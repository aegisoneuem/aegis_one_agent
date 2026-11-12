//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"golang.org/x/sys/windows"
)

// DEX reliability collector — counts stability events in the Windows System log
// via wevtapi (EvtQuery), a native flat C API. No PowerShell, no WMI/COM, so it
// does not inflate the process. Best-effort: any failure yields 0 for that count.
var (
	wevtapi       = windows.NewLazySystemDLL("wevtapi.dll")
	procEvtQuery  = wevtapi.NewProc("EvtQuery")
	procEvtNext   = wevtapi.NewProc("EvtNext")
	procEvtClose  = wevtapi.NewProc("EvtClose")
)

const (
	evtQueryChannelPath     = 0x1
	evtQueryForwardDirection = 0x100
	dexWindowDays           = 30
	dexCountSafetyCap       = 100000 // never loop forever on a pathological log
)

// collectDexHealth returns the device-stability signals DEX scores: counts of
// unexpected shutdowns, bugchecks (BSOD), and dirty power events over the last
// dexWindowDays.
func collectDexHealth() *pb.DexHealth {
	windowMs := int64(dexWindowDays) * 86400 * 1000
	return &pb.DexHealth{
		UnexpectedShutdowns: int32(countSystemEvents(6008, windowMs)),
		Bugchecks:           int32(countSystemEvents(1001, windowMs)),
		DirtyPowerEvents:    int32(countSystemEvents(41, windowMs)),
		WindowDays:          dexWindowDays,
	}
}

// countSystemEvents counts System-log events with the given EventID created
// within windowMs milliseconds of now, using a server-side XPath filter.
func countSystemEvents(eventID int, windowMs int64) int {
	query := fmt.Sprintf("*[System[(EventID=%d) and TimeCreated[timediff(@SystemTime) <= %d]]]", eventID, windowMs)
	chPtr, err := syscall.UTF16PtrFromString("System")
	if err != nil {
		return 0
	}
	qPtr, err := syscall.UTF16PtrFromString(query)
	if err != nil {
		return 0
	}
	h, _, _ := procEvtQuery.Call(
		0, // local session
		uintptr(unsafe.Pointer(chPtr)),
		uintptr(unsafe.Pointer(qPtr)),
		uintptr(evtQueryChannelPath|evtQueryForwardDirection),
	)
	if h == 0 {
		return 0 // channel missing / bad query — best-effort
	}
	defer procEvtClose.Call(h)

	count := 0
	const batch = 64
	var events [batch]uintptr
	for count < dexCountSafetyCap {
		var returned uint32
		r, _, _ := procEvtNext.Call(
			h, batch,
			uintptr(unsafe.Pointer(&events[0])),
			0, // timeout: unused for a query result set
			0,
			uintptr(unsafe.Pointer(&returned)),
		)
		if r == 0 || returned == 0 {
			break // ERROR_NO_MORE_ITEMS
		}
		for i := uint32(0); i < returned; i++ {
			procEvtClose.Call(events[i])
		}
		count += int(returned)
	}
	return count
}
