//go:build windows

package main

import (
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Minimal native password-prompt dialog (Win32) for the interactive MSI uninstall
// gate — so uninstalling from Add/Remove Programs shows a passcode prompt instead of
// a cryptic failure. No external deps; a plain modal window with a password edit.

var (
	pwUser32            = windows.NewLazySystemDLL("user32.dll")
	pwKernel32          = windows.NewLazySystemDLL("kernel32.dll")
	procRegisterClassEx = pwUser32.NewProc("RegisterClassExW")
	procCreateWindowEx  = pwUser32.NewProc("CreateWindowExW")
	procDefWindowProc   = pwUser32.NewProc("DefWindowProcW")
	procGetMessage      = pwUser32.NewProc("GetMessageW")
	procTranslateMsg    = pwUser32.NewProc("TranslateMessage")
	procDispatchMessage = pwUser32.NewProc("DispatchMessageW")
	procIsDialogMessage = pwUser32.NewProc("IsDialogMessageW")
	procDestroyWindow   = pwUser32.NewProc("DestroyWindow")
	procPostQuitMessage = pwUser32.NewProc("PostQuitMessage")
	procGetWindowText   = pwUser32.NewProc("GetWindowTextW")
	procSetFocus        = pwUser32.NewProc("SetFocus")
	procShowWindow      = pwUser32.NewProc("ShowWindow")
	procGetSystemMetric = pwUser32.NewProc("GetSystemMetrics")
	procSetForeground   = pwUser32.NewProc("SetForegroundWindow")
	procGetModuleHandle  = pwKernel32.NewProc("GetModuleHandleW")
	procGetConsoleWin    = pwKernel32.NewProc("GetConsoleWindow")
	procGetSysColorBrush = pwUser32.NewProc("GetSysColorBrush")
	pwGdi32              = windows.NewLazySystemDLL("gdi32.dll")
	procSetTextColor     = pwGdi32.NewProc("SetTextColor")
	procSetBkMode        = pwGdi32.NewProc("SetBkMode")
)

const (
	wmCtlColorStatic = 0x0138
	colorRed         = 0x000000FF // COLORREF 0x00BBGGRR → red
	bkTransparent    = 1
	colorBtnFace     = 15
)

// hideConsoleWindow hides this process's console window (the MSI launches the
// uninstall-check exe as a console app; its log window is ugly during the prompt).
func hideConsoleWindow() {
	if h, _, _ := procGetConsoleWin.Call(); h != 0 {
		procShowWindow.Call(h, 0) // SW_HIDE
	}
}

const (
	wsOverlapped   = 0x00000000
	wsCaption      = 0x00C00000
	wsSysMenu      = 0x00080000
	wsVisible      = 0x10000000
	wsChild        = 0x40000000
	wsBorder       = 0x00800000
	wsTabStop      = 0x00010000
	esPassword     = 0x0020
	esAutoHScroll  = 0x0080
	bsDefPush      = 0x00000001
	swShow         = 5
	wmDestroy      = 0x0002
	wmClose        = 0x0010
	wmCommand      = 0x0111
	idOK           = 1
	idCancel       = 2
	idEdit         = 101
	cwUseDefault   = ^uint32(0x7fffffff) // 0x80000000
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

// state for the single modal invocation (uninstall-check is single-threaded).
var (
	pwEditHwnd  windows.Handle
	pwLabelHwnd windows.Handle
	pwLabelRed  bool
	pwResult    string
	pwOK        bool
)

func promptPasswordDialog(title, prompt string, red bool) (string, bool) {
	// A Win32 window + its message loop MUST live on one OS thread; without this Go
	// may migrate the goroutine and messages stop pumping → "Not Responding"/hang.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pwResult, pwOK, pwEditHwnd, pwLabelHwnd, pwLabelRed = "", false, 0, 0, red
	hInst, _, _ := procGetModuleHandle.Call(0)
	className, _ := syscall.UTF16PtrFromString("AegisPwDlg")

	wndProc := windows.NewCallback(func(hwnd windows.Handle, msg uint32, wparam, lparam uintptr) uintptr {
		switch msg {
		case wmCommand:
			switch wparam & 0xFFFF {
			case idOK:
				buf := make([]uint16, 256)
				n, _, _ := procGetWindowText.Call(uintptr(pwEditHwnd), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
				pwResult = windows.UTF16ToString(buf[:n])
				pwOK = true
				procDestroyWindow.Call(uintptr(hwnd))
			case idCancel:
				procDestroyWindow.Call(uintptr(hwnd))
			}
			return 0
		case wmCtlColorStatic:
			if pwLabelRed && windows.Handle(lparam) == pwLabelHwnd {
				procSetTextColor.Call(wparam, colorRed)
				procSetBkMode.Call(wparam, bkTransparent)
				br, _, _ := procGetSysColorBrush.Call(colorBtnFace)
				return br
			}
		case wmClose:
			procDestroyWindow.Call(uintptr(hwnd))
			return 0
		case wmDestroy:
			procPostQuitMessage.Call(0)
			return 0
		}
		r, _, _ := procDefWindowProc.Call(uintptr(hwnd), uintptr(msg), wparam, lparam)
		return r
	})

	wc := wndClassEx{
		cbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		lpfnWndProc:   wndProc,
		hInstance:     windows.Handle(hInst),
		hbrBackground: windows.Handle(16), // COLOR_BTNFACE+1
		lpszClassName: className,
	}
	procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))

	titlePtr, _ := syscall.UTF16PtrFromString(title)
	scrW, _, _ := procGetSystemMetric.Call(0) // SM_CXSCREEN
	scrH, _, _ := procGetSystemMetric.Call(1) // SM_CYSCREEN
	w, h := 400, 190
	x := (int(scrW) - w) / 2
	y := (int(scrH) - h) / 2
	hwnd, _, _ := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(titlePtr)),
		wsCaption|wsSysMenu, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0, 0, hInst, 0)
	if hwnd == 0 {
		return "", false
	}

	mkChild := func(class, text string, style uint32, cx, cy, cw, ch, id int) windows.Handle {
		cp, _ := syscall.UTF16PtrFromString(class)
		tp, _ := syscall.UTF16PtrFromString(text)
		ch2, _, _ := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(cp)), uintptr(unsafe.Pointer(tp)),
			uintptr(wsChild|wsVisible|style), uintptr(cx), uintptr(cy), uintptr(cw), uintptr(ch),
			uintptr(hwnd), uintptr(id), hInst, 0)
		return windows.Handle(ch2)
	}
	pwLabelHwnd = mkChild("STATIC", prompt, 0, 15, 15, 360, 40, 102)
	pwEditHwnd = mkChild("EDIT", "", wsBorder|wsTabStop|esPassword|esAutoHScroll, 15, 65, 360, 24, idEdit)
	mkChild("BUTTON", "OK", wsTabStop|bsDefPush, 200, 110, 80, 28, idOK)
	mkChild("BUTTON", "Cancel", wsTabStop, 290, 110, 80, 28, idCancel)

	procShowWindow.Call(hwnd, swShow)
	procSetForeground.Call(hwnd)
	procSetFocus.Call(uintptr(pwEditHwnd))

	var msg msgStruct
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break // WM_QUIT or error
		}
		if ret, _, _ := procIsDialogMessage.Call(hwnd, uintptr(unsafe.Pointer(&msg))); ret != 0 {
			continue // dialog handled Tab/Enter/Esc
		}
		procTranslateMsg.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
	return pwResult, pwOK
}
