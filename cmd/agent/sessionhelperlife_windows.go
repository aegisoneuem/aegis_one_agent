//go:build windows

package main

import (
	"log/slog"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Service-alive beacon: a cross-session named event the SERVICE creates and holds
// open for its whole life. The tray helper (a separate process in the user session)
// polls for it; when the service process exits, its handle closes, the named object is
// destroyed, the poll fails, and the helper exits itself. This keeps the helper's
// lifetime tied to the service's — no orphaned helpers, no duplicate tray icons after
// a service restart. Cheap: a CreateEvent once, and a 5s OpenEvent+Close in the helper.
//
// The event lives in the Global\ namespace so Session 0 (service) and the user session
// can share it; a permissive DACL lets any interactive user OPEN it (existence check
// only — nothing is signalled through it).

const serviceAliveEventName = `Global\AegisOneClientAlive`

var (
	procOpenEvent    = pwKernel32.NewProc("OpenEventW")
	serviceAliveEvt  windows.Handle // held for the service process lifetime (never closed)
)

// startServiceAliveBeacon creates the named event (service side). No-op off-service.
func startServiceAliveBeacon() {
	if !runningAsService {
		return
	}
	sd, err := windows.SecurityDescriptorFromString("D:(A;;0x1F0003;;;SY)(A;;0x100000;;;WD)")
	if err != nil {
		return
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	name, err := windows.UTF16PtrFromString(serviceAliveEventName)
	if err != nil {
		return
	}
	h, err := windows.CreateEvent(sa, 1 /* manual reset */, 1 /* signalled */, name)
	if err != nil && h == 0 {
		slog.Debug("session-helper: could not create alive beacon", "error", err.Error())
		return
	}
	serviceAliveEvt = h // keep the handle open; closing it would defeat the beacon
}

// serviceAliveBeaconPresent returns true while the service's beacon exists (helper
// side). It opens then immediately closes the handle so the helper never keeps the
// object alive after the service is gone.
func serviceAliveBeaconPresent() bool {
	name, err := windows.UTF16PtrFromString(serviceAliveEventName)
	if err != nil {
		return true // can't tell — don't kill the helper on a transient error
	}
	r, _, _ := procOpenEvent.Call(uintptr(windows.SYNCHRONIZE), 0, uintptr(unsafe.Pointer(name)))
	if r == 0 {
		return false // not found → service gone
	}
	_ = windows.CloseHandle(windows.Handle(r))
	return true
}
