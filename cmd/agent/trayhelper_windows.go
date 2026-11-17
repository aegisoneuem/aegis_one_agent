//go:build windows

package main

import (
	"context"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Tray helper: the interactive-session side of the user-messaging subsystem. Launched
// by the service (sessionhelper_windows.go) as `AegisOneClient.exe tray-helper` inside
// the logged-on user's desktop, it shows the AegisOne tray icon and (in later steps)
// renders notifications/prompts the service sends over a local IPC. Step 1 = the tray
// presence itself. The same helper process will host Remote-Control capture later.

var (
	thShell32         = windows.NewLazySystemDLL("shell32.dll")
	procShellNotifyIcon = thShell32.NewProc("Shell_NotifyIconW")
	procLoadIcon        = pwUser32.NewProc("LoadIconW")
)

const (
	nimAdd        = 0x0
	nimModify     = 0x1
	nimDelete     = 0x2
	nimSetVersion = 0x4

	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4
	nifInfo    = 0x10

	niifInfo = 0x1

	idiShield = 32518 // IDI_SHIELD stock icon (a security shield)

	wmApp            = 0x8000
	trayCallbackMsg  = wmApp + 1
	notifyVersion4   = 4
	hwndMessageConst = ^uintptr(2) // HWND_MESSAGE == (HWND)-3
)

// notifyIconData mirrors NOTIFYICONDATAW (modern layout). cbSize is set from
// unsafe.Sizeof so it matches whatever this build compiles to.
type notifyIconData struct {
	cbSize            uint32
	hWnd              windows.Handle
	uID               uint32
	uFlags            uint32
	uCallbackMessage  uint32
	hIcon             windows.Handle
	szTip             [128]uint16
	dwState           uint32
	dwStateMask       uint32
	szInfo            [256]uint16
	uVersionOrTimeout uint32
	szInfoTitle       [64]uint16
	dwInfoFlags       uint32
	guidItem          windows.GUID
	hBalloonIcon      windows.Handle
}

var trayIconData notifyIconData // the single tray icon for this helper process

func copyUTF16(dst []uint16, s string) {
	src, _ := windows.UTF16FromString(s)
	for i := 0; i < len(src) && i < len(dst); i++ {
		dst[i] = src[i]
	}
}

// runTrayHelper shows the tray icon and pumps the Win32 message loop until the agent
// context is cancelled or the session ends. It must own its OS thread for the whole
// life of the window (Go would otherwise migrate the goroutine and stall the loop).
func runTrayHelper(ctx context.Context) {
	defer recoverLog("tray-helper")
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hideConsoleWindow() // launched with CREATE_NO_WINDOW, but be safe

	hInst, _, _ := procGetModuleHandle.Call(0)
	className, _ := windows.UTF16PtrFromString("AegisOneTrayHelper")

	wndProc := syscall.NewCallback(func(hwnd windows.Handle, msg uint32, wParam, lParam uintptr) uintptr {
		switch msg {
		case wmDestroy:
			procPostQuitMessage.Call(0)
			return 0
		case trayCallbackMsg:
			// Left/right click handling (context menu, open portal) arrives in a later
			// step; for now the icon is informational.
			return 0
		}
		r, _, _ := procDefWindowProc.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
		return r
	})

	wc := wndClassEx{
		cbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		lpfnWndProc:   wndProc,
		hInstance:     windows.Handle(hInst),
		lpszClassName: className,
	}
	procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))

	// A message-only window is enough to receive the tray callback.
	hwnd, _, _ := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, hwndMessageConst, 0, hInst, 0)
	if hwnd == 0 {
		return
	}

	hIcon, _, _ := procLoadIcon.Call(0, uintptr(idiShield))

	trayIconData = notifyIconData{
		hWnd:             windows.Handle(hwnd),
		uID:              1,
		uFlags:           nifMessage | nifIcon | nifTip,
		uCallbackMessage: trayCallbackMsg,
		hIcon:            windows.Handle(hIcon),
	}
	trayIconData.cbSize = uint32(unsafe.Sizeof(trayIconData))
	copyUTF16(trayIconData.szTip[:], "AegisOne Client — endpoint protection active")
	procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&trayIconData)))

	trayIconData.uVersionOrTimeout = notifyVersion4
	procShellNotifyIcon.Call(nimSetVersion, uintptr(unsafe.Pointer(&trayIconData)))

	defer procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&trayIconData)))

	// Exit cleanly when the agent context is cancelled (service stopping): post a
	// WM_DESTROY so the message loop returns and the icon is removed.
	go func() {
		<-ctx.Done()
		procDestroyWindow.Call(hwnd)
	}()

	// Message loop.
	var msg msgStruct
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 { // WM_QUIT or error
			return
		}
		procTranslateMsg.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// trayShowBalloon pops a balloon notification from the tray icon (used by the
// notification steps). Safe to call from the message-loop thread.
func trayShowBalloon(title, text string) {
	trayIconData.uFlags = nifInfo
	trayIconData.dwInfoFlags = niifInfo
	copyUTF16(trayIconData.szInfoTitle[:], title)
	copyUTF16(trayIconData.szInfo[:], text)
	procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&trayIconData)))
	trayIconData.uFlags = nifMessage | nifIcon | nifTip // restore
}
