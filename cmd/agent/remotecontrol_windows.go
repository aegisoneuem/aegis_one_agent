//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// wtsapi32 is resolved lazily so the agent still loads where the DLL is absent;
// WTSQuerySessionInformationW / WTSFreeMemory are not exported by the vendored
// x/sys/windows, so we bind them directly.
var (
	wtsapi32                        = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSQuerySessionInformationW = wtsapi32.NewProc("WTSQuerySessionInformationW")
	procWTSFreeMemory               = wtsapi32.NewProc("WTSFreeMemory")
)

// WTSUserName info class; WTS_CURRENT_SERVER_HANDLE == 0.
const wtsUserNameClass = 5

// detectInteractiveUser returns the user logged into the active physical console
// session and whether an interactive user is present. It is best-effort: any
// failure yields ("", false) rather than an error — RC-0 uses it only for the
// visible indicator and the session snapshot, and RC-2 will use the session for
// input-injection context (addendum #1: run as the interactive user, not SYSTEM).
func detectInteractiveUser() (string, bool) {
	sid := windows.WTSGetActiveConsoleSessionId()
	if sid == 0xFFFFFFFF { // no active console session (e.g. headless server)
		return "", false
	}
	var buf *uint16
	var n uint32
	r1, _, _ := procWTSQuerySessionInformationW.Call(
		0, // WTS_CURRENT_SERVER_HANDLE
		uintptr(sid),
		uintptr(wtsUserNameClass),
		uintptr(unsafe.Pointer(&buf)),
		uintptr(unsafe.Pointer(&n)),
	)
	if r1 == 0 || buf == nil {
		return "", false
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(buf)))
	user := windows.UTF16PtrToString(buf)
	return user, user != ""
}
