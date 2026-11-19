//go:build windows

package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
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
		UnexpectedShutdowns: int32(countWinEvents("System", "EventLog", 6008, windowMs)),
		Bugchecks:           int32(countWinEvents("System", "Microsoft-Windows-WER-SystemErrorReporting", 1001, windowMs)),
		DirtyPowerEvents:    int32(countWinEvents("System", "Microsoft-Windows-Kernel-Power", 41, windowMs)),
		AppCrashes:          int32(countWinEvents("Application", "Application Error", 1000, windowMs)),
		AppHangs:            int32(countWinEvents("Application", "Application Hang", 1002, windowMs)),
		AppFaults:           collectAppFaults(windowMs),
		WindowDays:          dexWindowDays,
		BootDurationMs:      bootDurationMs(),
	}
}

// eventXPath builds a channel XPath filtering on provider (optional), EventID,
// and the time window. Provider filtering matters because EventIDs like 1000/1002
// are reused by many providers — only "Application Error"/"Application Hang" are
// real app crashes/hangs.
func eventXPath(provider string, eventID int, windowMs int64) string {
	cond := fmt.Sprintf("(EventID=%d) and TimeCreated[timediff(@SystemTime) <= %d]", eventID, windowMs)
	if provider != "" {
		cond = fmt.Sprintf("Provider[@Name='%s'] and %s", provider, cond)
	}
	return "*[System[" + cond + "]]"
}

const (
	dexMaxFaultEvents = 2000 // render at most this many fault events (bound cost)
	dexMaxFaultApps   = 25   // report at most the top-N unstable apps
)

// faultEvent is one parsed Application Error/Hang event.
type faultEvent struct {
	app       string
	module    string // faulting module (crashes only)
	exception string // exception code (crashes only)
	unix      int64  // event time (unix seconds), 0 = unknown
}

// eventTimeRe pulls the event time from the rendered XML (<TimeCreated SystemTime=…>).
var eventTimeRe = regexp.MustCompile(`SystemTime=['"]([^'"]+)['"]`)

func parseEventUnix(xml string) int64 {
	m := eventTimeRe.FindStringSubmatch(xml)
	if len(m) < 2 {
		return 0
	}
	if t, err := time.Parse(time.RFC3339Nano, m[1]); err == nil {
		return t.Unix()
	}
	return 0
}

// appDataRe extracts the first <Data> value of an Application Error/Hang event —
// the faulting application name (e.g. "chrome.exe") sits in the first unnamed
// Data element for events 1000 and 1002.
var appDataRe = regexp.MustCompile(`<Data[^>]*>([^<]+)</Data>`)

// collectAppFaults returns the per-application crash/hang breakdown (top apps by
// total faults), so the console can show WHICH apps are unstable.
func collectAppFaults(windowMs int64) []*pb.AppFault {
	agg := map[string]*pb.AppFault{}
	get := func(name string) *pb.AppFault {
		f := agg[name]
		if f == nil {
			f = &pb.AppFault{Name: name}
			agg[name] = f
		}
		return f
	}
	forEachFaultEvent("Application", "Application Error", 1000, windowMs, func(fe faultEvent) {
		f := get(fe.app)
		f.Crashes++
		if f.Crashes == 1 { // first = newest (events are read reverse-chronological)
			f.LastModule, f.LastException, f.LastCrashUnix = fe.module, fe.exception, fe.unix
		}
	})
	forEachFaultEvent("Application", "Application Hang", 1002, windowMs, func(fe faultEvent) { get(fe.app).Hangs++ })

	out := make([]*pb.AppFault, 0, len(agg))
	for _, f := range agg {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := out[i].Crashes+out[i].Hangs, out[j].Crashes+out[j].Hangs
		if ti != tj {
			return ti > tj
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > dexMaxFaultApps {
		out = out[:dexMaxFaultApps]
	}
	return out
}

// forEachFaultEvent renders each matching event and calls fn with the parsed fault
// (app + crash module/exception/time). Bounded by dexMaxFaultEvents.
func forEachFaultEvent(channel, provider string, eventID int, windowMs int64, fn func(faultEvent)) {
	query := eventXPath(provider, eventID, windowMs)
	chPtr, err := syscall.UTF16PtrFromString(channel)
	if err != nil {
		return
	}
	qPtr, err := syscall.UTF16PtrFromString(query)
	if err != nil {
		return
	}
	h, _, _ := procEvtQuery.Call(0, uintptr(unsafe.Pointer(chPtr)), uintptr(unsafe.Pointer(qPtr)), uintptr(evtQueryChannelPath|evtQueryReverseDirection))
	if h == 0 {
		return
	}
	defer procEvtClose.Call(h)

	seen := 0
	const batch = 32
	var events [batch]uintptr
	for seen < dexMaxFaultEvents {
		var returned uint32
		r, _, _ := procEvtNext.Call(h, batch, uintptr(unsafe.Pointer(&events[0])), 0, 0, uintptr(unsafe.Pointer(&returned)))
		if r == 0 || returned == 0 {
			break
		}
		for i := uint32(0); i < returned; i++ {
			if xml := renderEventXML(events[i]); xml != "" {
				data := appDataRe.FindAllStringSubmatch(xml, -1)
				if len(data) >= 1 {
					if app := strings.TrimSpace(data[0][1]); app != "" {
						fe := faultEvent{app: app, unix: parseEventUnix(xml)}
						// Application Error (1000) Data order: 0=app,1=ver,2=ts,3=module,
						// 4=modver,5=modts,6=exception code. Hangs (1002) have no module.
						if len(data) >= 7 {
							fe.module = strings.TrimSpace(data[3][1])
							fe.exception = strings.TrimSpace(data[6][1])
						}
						fn(fe)
					}
				}
			}
			procEvtClose.Call(events[i])
			seen++
		}
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

// countWinEvents counts events from provider (optional) with the given EventID in
// the named channel, within windowMs, using a server-side XPath filter.
func countWinEvents(channel, provider string, eventID int, windowMs int64) int {
	query := eventXPath(provider, eventID, windowMs)
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
