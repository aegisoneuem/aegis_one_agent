//go:build windows

package main

import (
	"fmt"
	"regexp"
	"strconv"
	"syscall"
	"unsafe"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"golang.org/x/sys/windows"
)

// DEX reliability collector — reads stability signals from the Windows event log
// via wevtapi (EvtQuery/EvtRender), a native flat C API. No PowerShell, no
// WMI/COM, so it does not inflate the process. Best-effort: any failure yields 0.
var (
	wevtapi      = windows.NewLazySystemDLL("wevtapi.dll")
	procEvtQuery = wevtapi.NewProc("EvtQuery")
	procEvtNext  = wevtapi.NewProc("EvtNext")
	procEvtRender = wevtapi.NewProc("EvtRender")
	procEvtClose = wevtapi.NewProc("EvtClose")
)

const (
	evtQueryChannelPath      = 0x1
	evtQueryForwardDirection = 0x100
	evtQueryReverseDirection = 0x200
	evtRenderEventXML        = 0x1
	dexWindowDays            = 30
	dexCountSafetyCap        = 100000 // never loop forever on a pathological log
)

var bootTimeRe = regexp.MustCompile(`Name="BootTime">(\d+)<`)

// collectDexHealth returns the device-stability signals DEX scores: counts of
// unexpected shutdowns, bugchecks (BSOD), and dirty power events over the last
// dexWindowDays.
func collectDexHealth() *pb.DexHealth {
	windowMs := int64(dexWindowDays) * 86400 * 1000
	return &pb.DexHealth{
		UnexpectedShutdowns: int32(countChannelEvents("System", 6008, windowMs)),
		Bugchecks:           int32(countChannelEvents("System", 1001, windowMs)),
		DirtyPowerEvents:    int32(countChannelEvents("System", 41, windowMs)),
		AppCrashes:          int32(countChannelEvents("Application", 1000, windowMs)),
		AppHangs:            int32(countChannelEvents("Application", 1002, windowMs)),
		WindowDays:          dexWindowDays,
		BootDurationMs:      bootDurationMs(),
	}
}

// bootDurationMs returns the last boot time in ms from Diagnostics-Performance
// event 100 (BootTime), or 0 when unavailable (the channel is off on some SKUs,
// or Fast Startup / resume-from-hibernate produced no full boot event).
func bootDurationMs() int64 {
	xml := latestEventXML(`Microsoft-Windows-Diagnostics-Performance/Operational`, 100, int64(365)*86400*1000)
	if xml == "" {
		return 0
	}
	m := bootTimeRe.FindStringSubmatch(xml)
	if len(m) < 2 {
		return 0
	}
	v, _ := strconv.ParseInt(m[1], 10, 64)
	return v
}

// latestEventXML returns the rendered XML of the most recent event matching
// eventID in channel within windowMs, or "" on any failure.
func latestEventXML(channel string, eventID int, windowMs int64) string {
	query := fmt.Sprintf("*[System[(EventID=%d) and TimeCreated[timediff(@SystemTime) <= %d]]]", eventID, windowMs)
	chPtr, err := syscall.UTF16PtrFromString(channel)
	if err != nil {
		return ""
	}
	qPtr, err := syscall.UTF16PtrFromString(query)
	if err != nil {
		return ""
	}
	h, _, _ := procEvtQuery.Call(0, uintptr(unsafe.Pointer(chPtr)), uintptr(unsafe.Pointer(qPtr)), uintptr(evtQueryChannelPath|evtQueryReverseDirection))
	if h == 0 {
		return ""
	}
	defer procEvtClose.Call(h)

	var evt uintptr
	var returned uint32
	r, _, _ := procEvtNext.Call(h, 1, uintptr(unsafe.Pointer(&evt)), 0, 0, uintptr(unsafe.Pointer(&returned)))
	if r == 0 || returned == 0 {
		return ""
	}
	defer procEvtClose.Call(evt)
	return renderEventXML(evt)
}

// renderEventXML renders an event handle to its XML string (two-call sizing).
func renderEventXML(evt uintptr) string {
	var bufUsed, propCount uint32
	procEvtRender.Call(0, evt, evtRenderEventXML, 0, 0, uintptr(unsafe.Pointer(&bufUsed)), uintptr(unsafe.Pointer(&propCount)))
	if bufUsed == 0 || bufUsed > (4<<20) { // sanity cap 4 MiB
		return ""
	}
	buf := make([]uint16, bufUsed/2+1)
	r, _, _ := procEvtRender.Call(0, evt, evtRenderEventXML, uintptr(len(buf)*2), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bufUsed)), uintptr(unsafe.Pointer(&propCount)))
	if r == 0 {
		return ""
	}
	return windows.UTF16ToString(buf)
}

// countChannelEvents counts events with the given EventID in the named channel
// (e.g. "System", "Application") created within windowMs milliseconds of now,
// using a server-side XPath filter.
func countChannelEvents(channel string, eventID int, windowMs int64) int {
	query := fmt.Sprintf("*[System[(EventID=%d) and TimeCreated[timediff(@SystemTime) <= %d]]]", eventID, windowMs)
	chPtr, err := syscall.UTF16PtrFromString(channel)
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
