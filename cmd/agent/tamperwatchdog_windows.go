//go:build windows

package main

import "golang.org/x/sys/windows/registry"

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
