//go:build windows

package trayui

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/aegisoneuem/aegis_one_agent/internal/uimsg"
)

// humanizeSnooze turns a snooze duration in seconds into a dropdown label.
func humanizeSnooze(s int64) string {
	switch {
	case s < 3600:
		if mn := s / 60; mn == 1 {
			return "in 1 minute"
		} else {
			return fmt.Sprintf("in %d minutes", mn)
		}
	case s < 86400:
		if h := s / 3600; h == 1 {
			return "in 1 hour"
		} else {
			return fmt.Sprintf("in %d hours", h)
		}
	default:
		if d := s / 86400; d == 1 {
			return "in 1 day"
		} else {
			return fmt.Sprintf("in %d days", d)
		}
	}
}

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
	wmSetIcon  = 0x0080
	stmSetIcon = 0x0170
	wsExDlgModalFrame = 0x00000001
	wsVScroll  = 0x00200000
	cbsDropDownList = 0x0003
	cbAddString = 0x0143
	cbSetCurSel = 0x014E
	cbGetCurSel = 0x0147
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

type winRect struct{ left, top, right, bottom int32 }

type btnInfo struct {
	action string
	snooze int64
}

type dlgState struct {
	buttons    map[uintptr]btnInfo
	comboHwnd  uintptr // snooze dropdown (0 = none)
	snoozeOpts []int64 // seconds, parallel to the dropdown entries
	action     string
	snooze     int64
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
				// Snooze: read the chosen duration from the dropdown if present.
				if bi.action == "snoozed" && st.comboHwnd != 0 && len(st.snoozeOpts) > 0 {
					idx, _, _ := procSendMessage.Call(st.comboHwnd, cbGetCurSel, 0, 0)
					if i := int(int32(idx)); i >= 0 && i < len(st.snoozeOpts) {
						st.snooze = st.snoozeOpts[i]
					}
				}
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
	hasSnooze := m.Kind == uimsg.KindAction && m.AllowSnooze
	snoozeOpts := m.SnoozeOpts
	if hasSnooze && len(snoozeOpts) == 0 {
		snoozeOpts = []int64{60, 300, 1800, 3600, 10800, 21600, 43200, 86400} // 1m..24h
	}

	const (
		width  = 540
		margin = 24
		textX  = 84 // right of the icon
		textW  = width - textX - margin
		bh     = 32
		bw     = 120
		gap    = 10
		bodyH  = 54
	)

	// Top-down layout cursor → also yields the client height so nothing is clipped.
	titleY := 22
	bodyY := titleY + 34
	cur := bodyY + bodyH + 6
	detailY, deadlineY := 0, 0
	if m.Detail != "" {
		detailY = cur
		cur += 44
	}
	if m.DeadlineUnix > 0 {
		deadlineY = cur
		cur += 24
	}
	rowY := cur + 8
	clientH := rowY + bh + 16

	// Convert the desired CLIENT size into the full WINDOW size (so the title bar +
	// borders don't eat the buttons).
	style := uint32(wsCaption | wsSysMenu | wsVisible)
	r := winRect{0, 0, int32(width), int32(clientH)}
	procAdjustWindowRect.Call(uintptr(unsafe.Pointer(&r)), uintptr(style), 0)
	winW := int(r.right - r.left)
	winH := int(r.bottom - r.top)

	scrW, _, _ := procGetSysMetrics.Call(smCXScreen)
	scrH, _, _ := procGetSysMetrics.Call(smCYScreen)
	x := (int(scrW) - winW) / 2
	y := (int(scrH) - winH) / 2

	// Caption = product (BigFix-style); the action title is a bold heading inside.
	// WS_EX_DLGMODALFRAME + clearing WM_SETICON removes the stray default caption icon.
	captionPtr, _ := windows.UTF16PtrFromString("AegisOne Client")
	exStyle := uintptr(wsExDlgModalFrame)
	if m.Kind == uimsg.KindAction {
		exStyle |= wsExTopmost // action requests pop to the front
	}
	hwnd, _, _ := procCreateWindowEx.Call(exStyle, uintptr(unsafe.Pointer(dlgClassPtr)),
		uintptr(unsafe.Pointer(captionPtr)), uintptr(style),
		uintptr(x), uintptr(y), uintptr(winW), uintptr(winH), 0, 0, hInst, 0)
	if hwnd == 0 {
		return "dismissed", 0
	}
	procSendMessage.Call(hwnd, wmSetIcon, 0, 0) // ICON_SMALL = none
	procSendMessage.Call(hwnd, wmSetIcon, 1, 0) // ICON_BIG   = none

	st := &dlgState{buttons: map[uintptr]btnInfo{}, snoozeOpts: snoozeOpts}
	dlgMu.Lock()
	dlgStates[hwnd] = st
	dlgMu.Unlock()
	defer func() {
		dlgMu.Lock()
		delete(dlgStates, hwnd)
		dlgMu.Unlock()
	}()

	// Icon on the left, aligned with the title.
	iconCtrl, _, _ := procCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(mustUTF16("STATIC"))), uintptr(unsafe.Pointer(mustUTF16(""))),
		wsChild|wsVisible|ssIcon, margin, uintptr(titleY+2), 32, 32, hwnd, 0, hInst, 0)
	if iconCtrl != 0 && icon != 0 {
		procSendMessage.Call(iconCtrl, stmSetIcon, icon, 0)
	}

	// Bold title + wrapped body + optional detail + deadline.
	newControl(hwnd, hInst, titleFont, "STATIC", m.Title, wsChild|wsVisible|ssLeft, textX, titleY, textW, 30, 0)
	newControl(hwnd, hInst, bodyFont, "STATIC", m.Text, wsChild|wsVisible|ssLeft, textX, bodyY, textW, bodyH, 0)
	if detailY > 0 {
		newControl(hwnd, hInst, bodyFont, "STATIC", m.Detail, wsChild|wsVisible|ssLeft, textX, detailY, textW, 40, 0)
	}
	if deadlineY > 0 {
		dl := "This action runs automatically at " + time.Unix(m.DeadlineUnix, 0).Format("Mon 02 Jan, 3:04 PM") + "."
		newControl(hwnd, hInst, bodyFont, "STATIC", dl, wsChild|wsVisible|ssLeft, textX, deadlineY, textW, 20, 0)
	}

	// Bottom row: action buttons on the right.
	bx := width - margin - bw
	for i := len(labels) - 1; i >= 0; i-- {
		id := uintptr(100 + i)
		bstyle := uint32(wsChild | wsVisible | wsTabStop)
		if i == 0 {
			bstyle |= bsDefPushBtn
		}
		st.buttons[id] = btnInfo{action: labels[i]}
		newControl(hwnd, hInst, bodyFont, "BUTTON", labels[i], bstyle, bx, rowY, bw, bh, id)
		bx -= bw + gap
	}
	// Snooze dropdown + button on the left (BigFix "remind me again" style).
	if hasSnooze {
		combo, _, _ := procCreateWindowEx.Call(0,
			uintptr(unsafe.Pointer(mustUTF16("COMBOBOX"))), uintptr(unsafe.Pointer(mustUTF16(""))),
			wsChild|wsVisible|wsTabStop|cbsDropDownList|wsVScroll,
			margin, uintptr(rowY+2), 150, 220, hwnd, 0, hInst, 0)
		procSendMessage.Call(combo, wmSetFont, bodyFont, 1)
		for _, s := range snoozeOpts {
			procSendMessage.Call(combo, cbAddString, 0, uintptr(unsafe.Pointer(mustUTF16(humanizeSnooze(s)))))
		}
		procSendMessage.Call(combo, cbSetCurSel, 0, 0)
		st.comboHwnd = combo
		st.buttons[snoozeButtonID] = btnInfo{action: "snoozed", snooze: snoozeOpts[0]}
		newControl(hwnd, hInst, bodyFont, "BUTTON", "Snooze", wsChild|wsVisible|wsTabStop, margin+158, rowY, 92, bh, snoozeButtonID)
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
