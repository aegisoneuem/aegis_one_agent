//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// wtsapi32 is resolved lazily so the agent still loads where the DLL is absent;
// these entry points are not exported by the vendored x/sys/windows.
var (
	wtsapi32                        = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSQuerySessionInformationW = wtsapi32.NewProc("WTSQuerySessionInformationW")
	procWTSEnumerateSessionsW       = wtsapi32.NewProc("WTSEnumerateSessionsW")
	procWTSFreeMemory               = wtsapi32.NewProc("WTSFreeMemory")
)

const (
	wtsUserNameClass = 5 // WTSUserName info class; WTS_CURRENT_SERVER_HANDLE == 0
	wtsActiveState   = 0 // WTSActive (a logged-in, connected session)
)

// wtsSessionInfoW mirrors WTS_SESSION_INFOW { DWORD SessionId; LPWSTR
// pWinStationName; WTS_CONNECTSTATE_CLASS State }. Go aligns the pointer to 8
// bytes on amd64, matching the C padding, so the struct size is correct.
type wtsSessionInfoW struct {
	SessionID      uint32
	WinStationName *uint16
	State          uint32
}

// detectInteractiveUser returns the interactive logged-in user and whether one is
// present. It prefers the physical console session, then falls back to ANY active
// session (RDP, Fast User Switching) — a server reached over RDP, or a console
// with no one at the physical screen, would otherwise report an empty user.
// Best-effort: any failure yields ("", false).
func detectInteractiveUser() (string, bool) {
	if sid := windows.WTSGetActiveConsoleSessionId(); sid != 0xFFFFFFFF {
		if u := wtsSessionUser(sid); u != "" {
			return u, true
		}
	}
	// Enumerate all sessions; return the first ACTIVE one that has a username.
	var p *wtsSessionInfoW
	var count uint32
	r, _, _ := procWTSEnumerateSessionsW.Call(
		0, // WTS_CURRENT_SERVER_HANDLE
		0, // Reserved
		1, // Version
		uintptr(unsafe.Pointer(&p)),
		uintptr(unsafe.Pointer(&count)),
	)
	if r == 0 || p == nil {
		return "", false
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(p)))
	for _, s := range unsafe.Slice(p, count) {
		if s.State == wtsActiveState {
			if u := wtsSessionUser(s.SessionID); u != "" {
				return u, true
			}
		}
	}
	return "", false
}

// wtsSessionUser returns the username for a session id, or "" on any failure.
func wtsSessionUser(sid uint32) string {
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
		return ""
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(buf)))
	return windows.UTF16PtrToString(buf)
}
