//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/mgr"
)

// restoreServiceIfMissing re-registers the agent service if an admin took ownership
// of the service registry key and DELETED the whole service definition. The running
// process survives such a delete (it is already loaded in memory) but would NOT start
// on the next boot — this recreates the service with the same config as `install` and
// re-locks it, restoring boot-persistence. Cheap: one OpenService probe each tick;
// only acts when the service is actually gone. Returns true when it had to recreate.
//
// Honest bound: if the admin ALSO deletes the exe and its backup, or keeps deleting
// faster than the 30s tick, this can't win — that is the server-redeploy / Phase-B
// boundary. This closes the common "deleted the service key" boot-persistence gap.
func restoreServiceIfMissing() bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()
	if s, err := m.OpenService(serviceName); err == nil {
		s.Close()
		return false // service present — nothing to do
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	s, err := createAgentService(m, exe)
	if err != nil {
		return false
	}
	s.Close()
	applyServiceDACLForState(true) // re-lock the freshly-created service + its registry key
	return true
}

// reEnableServiceStart restores the service's start type to AUTO if an admin set it
// to DISABLED (sc config start= disabled, or regedit Start=4) — which the service
// DACL does NOT prevent (it guards the SCM object, not the Services registry key).
// The agent runs as SYSTEM, which has write access to the Services key. Returns true
// when it had to re-enable (i.e. tamper was detected).
func reEnableServiceStart() bool {
	const (
		startDisabled = 4
		startAuto      = 2
	)
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\`+serviceName, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	start, _, err := k.GetIntegerValue("Start")
	if err != nil {
		return false
	}
	if start == startDisabled {
		if err := k.SetDWordValue("Start", startAuto); err == nil {
			return true
		}
	}
	return false
}
