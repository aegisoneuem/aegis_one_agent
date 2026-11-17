//go:build windows

package main

import (
	"log/slog"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/aegisoneuem/aegis_one_agent/internal/trayui"
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

// serviceAliveEventName is shared with the tray UI so the beacon name never drifts.
const serviceAliveEventName = trayui.ServiceAliveEventName

// serviceAliveEvt is held for the service process lifetime (never closed) — when the
// process exits, the handle closes, the named event vanishes, and the tray UI (which
// polls it) exits. The POLL side lives in internal/trayui.
var serviceAliveEvt windows.Handle

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
