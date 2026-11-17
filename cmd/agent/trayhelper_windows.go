//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Tray helper: the interactive-session side of the user-messaging subsystem. Launched
// by the service (sessionhelper_windows.go) as `AegisOneClient.exe tray-helper` inside
// the logged-on user's desktop, it shows the AegisOne tray icon and (in later steps)
// renders notifications/prompts the service sends over a local IPC. Step 1 = the tray
// presence itself. The same helper process will host Remote-Control capture later.

var (
	thShell32            = windows.NewLazySystemDLL("shell32.dll")
	procShellNotifyIcon  = thShell32.NewProc("Shell_NotifyIconW")
	procExtractIcon      = thShell32.NewProc("ExtractIconW")
	procLoadIcon         = pwUser32.NewProc("LoadIconW")
	procRegisterWindowMsg = pwUser32.NewProc("RegisterWindowMessageW")
	procPostMessage      = pwUser32.NewProc("PostMessageW")
)

// trayDebugf appends a line to a diagnostic log (the helper has no console). Best
// effort; used to confirm the tray icon was added / why it was not.
func trayDebugf(format string, a ...any) {
	f, err := os.OpenFile(`C:\Windows\Temp\aegis-trayhelper.log`, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, time.Now().Format("15:04:05")+" "+format+"\n", a...)
}

// loadTrayIcon returns the agent exe's own embedded icon (aegisone.ico), falling back
// to the stock application icon if extraction fails.
func loadTrayIcon(hInst uintptr) uintptr {
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

// taskbarCreatedMsg is the "TaskbarCreated" broadcast the shell sends when Explorer
// (re)starts; on it we must re-add our tray icon or it vanishes after an Explorer
// restart. Resolved once at helper start.
var taskbarCreatedMsg uint32

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

	idiApplication = 32512 // IDI_APPLICATION stock icon (fallback)

	wmApp            = 0x8000
	trayCallbackMsg  = wmApp + 1
	notifyVersion4 = 4
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
		switch {
		case msg == wmDestroy:
			procPostQuitMessage.Call(0)
			return 0
		case msg == trayCallbackMsg:
			// Left/right click handling (context menu, open portal) arrives in a later
			// step; for now the icon is informational.
			return 0
		case taskbarCreatedMsg != 0 && msg == taskbarCreatedMsg:
			// Explorer restarted — re-add the icon so it reappears.
			procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&trayIconData)))
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

	if tbName, err := windows.UTF16PtrFromString("TaskbarCreated"); err == nil {
		r, _, _ := procRegisterWindowMsg.Call(uintptr(unsafe.Pointer(tbName)))
		taskbarCreatedMsg = uint32(r)
	}

	// A normal top-level window (created hidden — never shown) reliably hosts a tray
	// icon; a message-only window does not receive the shell's icon plumbing.
	hwnd, _, _ := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(className)), wsOverlapped, 0, 0, 0, 0, 0, 0, hInst, 0)
	if hwnd == 0 {
		return
	}

	hIcon := loadTrayIcon(hInst)

	trayIconData = notifyIconData{
		hWnd:             windows.Handle(hwnd),
		uID:              1,
		uFlags:           nifMessage | nifIcon | nifTip,
		uCallbackMessage: trayCallbackMsg,
		hIcon:            windows.Handle(hIcon),
	}
	trayIconData.cbSize = uint32(unsafe.Sizeof(trayIconData))
	copyUTF16(trayIconData.szTip[:], "AegisOne Client — endpoint protection active")
	addOK, _, addErr := procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&trayIconData)))
	trayDebugf("NIM_ADD ok=%d hwnd=%d hIcon=%d err=%v", addOK, hwnd, hIcon, addErr)

	trayIconData.uVersionOrTimeout = notifyVersion4
	procShellNotifyIcon.Call(nimSetVersion, uintptr(unsafe.Pointer(&trayIconData)))

	defer procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&trayIconData)))

	// A one-time startup balloon confirms the helper is live even when Windows tucks
	// the icon into the overflow ("^") area, where new icons go by default.
	trayShowBalloon("AegisOne Client", "Endpoint protection is active.")

	// Exit cleanly when the context is cancelled OR the service's alive beacon
	// disappears (service stopped/crashed) — so the helper never lingers orphaned and
	// a restart never leaves two tray icons. NOTE: DestroyWindow only works on the
	// thread that created the window; from this goroutine we must POST a WM_CLOSE so
	// the message-loop thread tears the window down (DefWindowProc -> WM_DESTROY ->
	// PostQuitMessage). Calling DestroyWindow here would be silently ignored.
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				procPostMessage.Call(hwnd, wmClose, 0, 0)
				return
			case <-t.C:
				if !serviceAliveBeaconPresent() {
					trayDebugf("alive beacon gone — posting WM_CLOSE to exit helper")
					procPostMessage.Call(hwnd, wmClose, 0, 0)
					return
				}
			}
		}
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
