//go:build windows

package trayui

import (
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/aegisoneuem/aegis_one_agent/internal/uimsg"
)

// Native message / action-request windows (BigFix "Action Requests" style). A
// KindMessage shows text the user reads and closes; a KindAction shows choice buttons
// (+ optional deadline + Snooze) and returns the user's choice. Each window runs on
// its OWN locked thread with its own message loop, independent of the tray icon loop.

const (
	wsCaption   = 0x00C00000
	wsSysMenu   = 0x00080000
	wsVisible   = 0x10000000
	wsChild     = 0x40000000
	wsTabStop   = 0x00010000
	wsExTopmost = 0x00000008

	ssLeft     = 0x0
	ssIcon     = 0x03
	bsDefPushBtn = 0x1
	wmCommand  = 0x0111
	wmSetFont  = 0x0030
	stmSetIcon = 0x0170
	swShow     = 5
	smCXScreen = 0
	smCYScreen = 1
	fwBold     = 700
	fwNormal   = 400
	defaultCharset = 1

	snoozeButtonID = 200
	snoozeSeconds  = 1800 // 30 minutes
)

// makeFont creates a Segoe UI font at the given point-ish height (negative = cell
// height) and weight. Falls back to 0 (caller then uses the default) on failure.
func makeFont(height, weight int) uintptr {
	face, _ := windows.UTF16PtrFromString("Segoe UI")
	h, _, _ := procCreateFont.Call(
		uintptr(int32(height)), 0, 0, 0, uintptr(weight), 0, 0, 0,
		defaultCharset, 0, 0, 0 /* CLEARTYPE_QUALITY */, 0,
		uintptr(unsafe.Pointer(face)))
	return h
}

type btnInfo struct {
	action string
	snooze int64
}

type dlgState struct {
	buttons map[uintptr]btnInfo
	action  string
	snooze  int64
}

var (
	dlgMu       sync.Mutex
	dlgStates   = map[uintptr]*dlgState{}
	dlgClassReg sync.Once
	dlgClassPtr *uint16
)

func dlgWndProc(hwnd windows.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmCommand:
		id := wParam & 0xffff
		dlgMu.Lock()
		st := dlgStates[uintptr(hwnd)]
		dlgMu.Unlock()
		if st != nil {
			if bi, ok := st.buttons[id]; ok {
				st.action, st.snooze = bi.action, bi.snooze
				procDestroyWindow.Call(uintptr(hwnd))
			}
		}
		return 0
	case wmClose:
		dlgMu.Lock()
		st := dlgStates[uintptr(hwnd)]
		dlgMu.Unlock()
		if st != nil && st.action == "" {
			st.action = "dismissed" // closed with X without choosing
		}
		procDestroyWindow.Call(uintptr(hwnd))
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProc.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return r
}

func ensureDlgClass(hInst uintptr) {
	dlgClassReg.Do(func() {
		dlgClassPtr, _ = windows.UTF16PtrFromString("AegisOneDlgWnd")
		wc := wndClassEx{
			cbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
			lpfnWndProc:   syscall.NewCallback(dlgWndProc),
			hInstance:     windows.Handle(hInst),
			lpszClassName: dlgClassPtr,
			hbrBackground: windows.Handle(16), // COLOR_BTNFACE+1
		}
		procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
	})
}

func mustUTF16(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

func newControl(parent, hInst, hFont uintptr, class, text string, style uint32, x, y, w, h int, id uintptr) {
	cp, _ := windows.UTF16PtrFromString(class)
	tp, _ := windows.UTF16PtrFromString(text)
	ctrl, _, _ := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(cp)), uintptr(unsafe.Pointer(tp)),
		uintptr(style), uintptr(x), uintptr(y), uintptr(w), uintptr(h), parent, id, hInst, 0)
	if ctrl != 0 && hFont != 0 {
		procSendMessage.Call(ctrl, wmSetFont, hFont, 1)
	}
}

// runDialog shows a message/action window and blocks (on its own thread) until the
// user chooses or closes it. Returns the chosen action label (or "dismissed") and any
// snooze duration in seconds.
func runDialog(m uimsg.Message) (string, int64) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hInst, _, _ := procGetModuleHandle.Call(0)
	ensureDlgClass(hInst)
	titleFont := makeFont(-21, fwBold)  // ~15.5pt bold heading
	bodyFont := makeFont(-16, fwNormal) // ~12pt body / buttons
	icon := loadIcon(hInst)             // the 'A' mark

	// Buttons: an action's own choices, else a single Close (a plain message).
	labels := m.Buttons
	if m.Kind != uimsg.KindAction || len(labels) == 0 {
		labels = []string{"Close"}
	}

	// Clean layout: icon on the left, bold title, wrapped body, deadline, buttons on
	// the bottom-right. Height grows with a deadline line.
	const (
		width   = 500
		margin  = 24
		textX   = 84 // right of the icon
		textW   = width - textX - margin
	)
	bodyY := 70
	bodyH := 96
	height := 236
	if m.DeadlineUnix > 0 {
		height = 268
	}

	scrW, _, _ := procGetSysMetrics.Call(smCXScreen)
	scrH, _, _ := procGetSysMetrics.Call(smCYScreen)
	x := (int(scrW) - width) / 2
	y := (int(scrH) - height) / 2

	// Caption = product (BigFix-style "BigFix Action Requests"); the action title is a
	// bold heading inside, so it never looks duplicated.
	captionPtr, _ := windows.UTF16PtrFromString("AegisOne Client")
	exStyle := uintptr(0)
	if m.Kind == uimsg.KindAction {
		exStyle = wsExTopmost // action requests pop to the front
	}
	hwnd, _, _ := procCreateWindowEx.Call(exStyle, uintptr(unsafe.Pointer(dlgClassPtr)),
		uintptr(unsafe.Pointer(captionPtr)), wsCaption|wsSysMenu|wsVisible,
		uintptr(x), uintptr(y), width, uintptr(height), 0, 0, hInst, 0)
	if hwnd == 0 {
		return "dismissed", 0
	}

	st := &dlgState{buttons: map[uintptr]btnInfo{}}
	dlgMu.Lock()
	dlgStates[hwnd] = st
	dlgMu.Unlock()
	defer func() {
		dlgMu.Lock()
		delete(dlgStates, hwnd)
		dlgMu.Unlock()
	}()

	// Icon (32x32) on the left.
	iconCtrl, _, _ := procCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(mustUTF16("STATIC"))), uintptr(unsafe.Pointer(mustUTF16(""))),
		wsChild|wsVisible|ssIcon, margin, 26, 32, 32, hwnd, 0, hInst, 0)
	if iconCtrl != 0 && icon != 0 {
		procSendMessage.Call(iconCtrl, stmSetIcon, icon, 0)
	}

	// Bold title + wrapped body + optional deadline.
	newControl(hwnd, hInst, titleFont, "STATIC", m.Title, wsChild|wsVisible|ssLeft, textX, 24, textW, 30, 0)
	newControl(hwnd, hInst, bodyFont, "STATIC", m.Text, wsChild|wsVisible|ssLeft, textX, bodyY, textW, bodyH, 0)
	if m.DeadlineUnix > 0 {
		dl := "This action runs automatically at " + time.Unix(m.DeadlineUnix, 0).Format("Mon 02 Jan, 3:04 PM") + "."
		newControl(hwnd, hInst, bodyFont, "STATIC", dl, wsChild|wsVisible|ssLeft, textX, bodyY+bodyH+6, textW, 20, 0)
	}

	// Buttons, right-aligned along the bottom.
	const bw, bh, gap = 124, 32, 10
	bx := width - margin - bw
	by := height - bh - 20
	for i := len(labels) - 1; i >= 0; i-- {
		id := uintptr(100 + i)
		style := uint32(wsChild | wsVisible | wsTabStop)
		if i == 0 {
			style |= bsDefPushBtn
		}
		st.buttons[id] = btnInfo{action: labels[i]}
		newControl(hwnd, hInst, bodyFont, "BUTTON", labels[i], style, bx, by, bw, bh, id)
		bx -= bw + gap
	}
	if m.Kind == uimsg.KindAction && m.AllowSnooze {
		st.buttons[snoozeButtonID] = btnInfo{action: "snoozed", snooze: snoozeSeconds}
		newControl(hwnd, hInst, bodyFont, "BUTTON", "Snooze 30m", wsChild|wsVisible|wsTabStop, margin, by, bw, bh, snoozeButtonID)
	}

	procSetForeground.Call(hwnd)
	procShowWindow.Call(hwnd, swShow)
	procUpdateWindow.Call(hwnd)

	var msg msgStruct
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMsg.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
	if st.action == "" {
		st.action = "dismissed"
	}
	return st.action, st.snooze
}
