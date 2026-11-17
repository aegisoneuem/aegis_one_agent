//go:build windows

// Package trayui is the interactive-session tray UI for the Aegis One agent. It is
// built into its own small signed binary (AegisOneClientUI.exe) and launched by the
// service into each logged-on user's desktop (the service lives in Session 0 and has
// no interactive desktop). Keeping it a separate binary gives it a distinct process
// name + file description and lets it be Authenticode-signed on its own, the way
// BigFix ships BESClientUI.exe alongside BESClient.exe.
package trayui

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ServiceAliveEventName is the cross-session beacon the SERVICE creates and holds
// open; the tray UI polls it and exits when it disappears (service stopped/crashed),
// so no orphaned helper and no duplicate tray icons after a restart.
const ServiceAliveEventName = `Global\AegisOneClientAlive`

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")

	procRegisterClassEx = user32.NewProc("RegisterClassExW")
	procCreateWindowEx  = user32.NewProc("CreateWindowExW")
	procDefWindowProc   = user32.NewProc("DefWindowProcW")
	procGetMessage      = user32.NewProc("GetMessageW")
	procTranslateMsg    = user32.NewProc("TranslateMessage")
	procDispatchMessage = user32.NewProc("DispatchMessageW")
	procPostQuitMessage = user32.NewProc("PostQuitMessage")
	procPostMessage     = user32.NewProc("PostMessageW")
	procLoadIcon        = user32.NewProc("LoadIconW")
	procRegisterWinMsg  = user32.NewProc("RegisterWindowMessageW")
	procShowWindow      = user32.NewProc("ShowWindow")

	procGetModuleHandle = kernel32.NewProc("GetModuleHandleW")
	procGetConsoleWin   = kernel32.NewProc("GetConsoleWindow")
	procOpenEvent       = kernel32.NewProc("OpenEventW")

	procShellNotifyIcon = shell32.NewProc("Shell_NotifyIconW")
	procExtractIcon     = shell32.NewProc("ExtractIconW")
)

const (
	wsOverlapped = 0x00000000
	wmDestroy    = 0x0002
	wmClose      = 0x0010

	nimAdd        = 0x0
	nimModify     = 0x1
	nimDelete     = 0x2
	nimSetVersion = 0x4

	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4
	nifInfo    = 0x10
	niifInfo   = 0x1

	idiApplication  = 32512
	wmApp           = 0x8000
	trayCallbackMsg = wmApp + 1
	notifyVersion4  = 4
	synchronize     = 0x00100000
)

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

type msgStruct struct {
	hwnd    windows.Handle
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
}

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

var (
	iconData         notifyIconData
	taskbarCreatedMsg uint32
)

func debugf(format string, a ...any) {
	f, err := os.OpenFile(`C:\Windows\Temp\aegis-trayhelper.log`, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, time.Now().Format("15:04:05")+" "+format+"\n", a...)
}

// CopyUTF16 writes s (UTF-16, NUL-terminated) into dst, never overflowing it.
func CopyUTF16(dst []uint16, s string) {
	src, _ := windows.UTF16FromString(s)
	for i := 0; i < len(src) && i < len(dst); i++ {
		dst[i] = src[i]
	}
}

func loadIcon(hInst uintptr) uintptr {
	if exe, err := os.Executable(); err == nil {
		if p, perr := windows.UTF16PtrFromString(exe); perr == nil {
			if h, _, _ := procExtractIcon.Call(hInst, uintptr(unsafe.Pointer(p)), 0); h > 1 {
				return h
			}
		}
	}
	h, _, _ := procLoadIcon.Call(0, uintptr(idiApplication))
	return h
}

func hideConsole() {
	if h, _, _ := procGetConsoleWin.Call(); h != 0 {
		procShowWindow.Call(h, 0) // SW_HIDE
	}
}

// beaconPresent reports whether the service's alive beacon still exists. It opens then
// immediately closes the handle so the UI never keeps the object alive past the
// service.
func beaconPresent() bool {
	name, err := windows.UTF16PtrFromString(ServiceAliveEventName)
	if err != nil {
		return true // can't tell — don't kill the UI on a transient error
	}
	r, _, _ := procOpenEvent.Call(uintptr(synchronize), 0, uintptr(unsafe.Pointer(name)))
	if r == 0 {
		return false
	}
	_ = windows.CloseHandle(windows.Handle(r))
	return true
}

func showBalloon(title, text string) {
	iconData.uFlags = nifInfo
	iconData.dwInfoFlags = niifInfo
	CopyUTF16(iconData.szInfoTitle[:], title)
	CopyUTF16(iconData.szInfo[:], text)
	procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&iconData)))
	iconData.uFlags = nifMessage | nifIcon | nifTip // restore
}

// Run shows the AegisOne tray icon and pumps the Win32 message loop until the service
// stops (its alive beacon vanishes) or the window is destroyed. It owns its OS thread
// for the window's whole life.
func Run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hideConsole()

	hInst, _, _ := procGetModuleHandle.Call(0)
	className, _ := windows.UTF16PtrFromString("AegisOneTrayUI")

	wndProc := syscall.NewCallback(func(hwnd windows.Handle, msg uint32, wParam, lParam uintptr) uintptr {
		switch {
		case msg == wmDestroy:
			procPostQuitMessage.Call(0)
			return 0
		case msg == trayCallbackMsg:
			return 0 // click handling (menu/portal) lands in a later step
		case taskbarCreatedMsg != 0 && msg == taskbarCreatedMsg:
			procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&iconData)))
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

	if tb, err := windows.UTF16PtrFromString("TaskbarCreated"); err == nil {
		r, _, _ := procRegisterWinMsg.Call(uintptr(unsafe.Pointer(tb)))
		taskbarCreatedMsg = uint32(r)
	}

	// A normal top-level window, created hidden (never shown), reliably hosts a tray
	// icon; a message-only window does not get the shell's icon plumbing.
	hwnd, _, _ := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(className)), wsOverlapped, 0, 0, 0, 0, 0, 0, hInst, 0)
	if hwnd == 0 {
		debugf("CreateWindowEx failed")
		return
	}

	hIcon := loadIcon(hInst)
	iconData = notifyIconData{
		hWnd:             windows.Handle(hwnd),
		uID:              1,
		uFlags:           nifMessage | nifIcon | nifTip,
		uCallbackMessage: trayCallbackMsg,
		hIcon:            windows.Handle(hIcon),
	}
	iconData.cbSize = uint32(unsafe.Sizeof(iconData))
	CopyUTF16(iconData.szTip[:], "AegisOne Client — endpoint protection active")
	addOK, _, addErr := procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&iconData)))
	debugf("NIM_ADD ok=%d hwnd=%d hIcon=%d err=%v", addOK, hwnd, hIcon, addErr)

	iconData.uVersionOrTimeout = notifyVersion4
	procShellNotifyIcon.Call(nimSetVersion, uintptr(unsafe.Pointer(&iconData)))
	defer procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&iconData)))

	// Startup balloon confirms the UI is live even when Windows hides a new icon in
	// the overflow ("^") area.
	showBalloon("AegisOne Client", "Endpoint protection is active.")

	// Exit when the service's beacon disappears. DestroyWindow only works on the
	// window's own thread, so POST a WM_CLOSE from here instead.
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for range t.C {
			if !beaconPresent() {
				debugf("alive beacon gone — posting WM_CLOSE")
				procPostMessage.Call(hwnd, wmClose, 0, 0)
				return
			}
		}
	}()

	var msg msgStruct
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		procTranslateMsg.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
}
