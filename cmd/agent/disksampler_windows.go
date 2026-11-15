//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Native DEX disk-performance sampler via PDH (Performance Data Helper). Reads the
// PhysicalDisk(_Total) counters — queue depth, IOPS, latency — with NO WMI and NO
// PowerShell, so sampling stays cheap. English counter names are used
// (PdhAddEnglishCounter) so it works regardless of the OS display language.
var (
	pdhDLL                          = windows.NewLazySystemDLL("pdh.dll")
	procPdhOpenQuery                = pdhDLL.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounter        = pdhDLL.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData         = pdhDLL.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterValue = pdhDLL.NewProc("PdhGetFormattedCounterValue")
	procPdhCloseQuery               = pdhDLL.NewProc("PdhCloseQuery")
)

const pdhFmtDouble = 0x00000200

// pdhFmtCounterValueDouble mirrors PDH_FMT_COUNTERVALUE with the double member. On
// 64-bit the union is 8-byte aligned, so there is 4 bytes of padding after CStatus.
type pdhFmtCounterValueDouble struct {
	CStatus     uint32
	_           uint32 // alignment padding before the 8-byte double
	DoubleValue float64
}

// diskSample is one PhysicalDisk(_Total) performance reading.
type diskSample struct {
	QueueLength float64 // avg. disk queue length (outstanding I/O requests)
	IOPS        float64 // disk transfers/sec
	LatencyMs   float64 // avg. ms per transfer
	OK          bool    // false on the first sample (rate counters need a delta) or if PDH is unavailable
}

// diskSampler wraps a persistent PDH query over the PhysicalDisk(_Total) counters.
// Rate counters need two collects separated by time, so the query is primed at
// construction and each sample() does one more collect (the delta since the last).
type diskSampler struct {
	query   uintptr
	queue   uintptr
	iops    uintptr
	latency uintptr
	ready   bool
}

// newDiskSampler opens the PDH query and adds the three counters. On any failure
// it returns a sampler whose sample() yields OK=false (disk metrics are then
// simply absent — CPU/memory sampling is unaffected).
func newDiskSampler() *diskSampler {
	d := &diskSampler{}
	if r, _, _ := procPdhOpenQuery.Call(0, 0, uintptr(unsafe.Pointer(&d.query))); r != 0 {
		return d
	}
	add := func(path string, h *uintptr) bool {
		p, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return false
		}
		r, _, _ := procPdhAddEnglishCounter.Call(d.query, uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(h)))
		return r == 0
	}
	okQ := add(`\PhysicalDisk(_Total)\Avg. Disk Queue Length`, &d.queue)
	okI := add(`\PhysicalDisk(_Total)\Disk Transfers/sec`, &d.iops)
	okL := add(`\PhysicalDisk(_Total)\Avg. Disk sec/Transfer`, &d.latency)
	if !okQ || !okI || !okL {
		procPdhCloseQuery.Call(d.query)
		d.query = 0
		return d
	}
	// Prime so the first real sample() has a delta for the rate counters.
	procPdhCollectQueryData.Call(d.query)
	d.ready = true
	return d
}

// sample collects the counters and returns the reading. OK is false until the
// first delta is available, or if PDH could not be set up.
func (d *diskSampler) sample() diskSample {
	if !d.ready {
		return diskSample{}
	}
	if r, _, _ := procPdhCollectQueryData.Call(d.query); r != 0 {
		return diskSample{}
	}
	read := func(h uintptr) (float64, bool) {
		var v pdhFmtCounterValueDouble
		r, _, _ := procPdhGetFormattedCounterValue.Call(h, pdhFmtDouble, 0, uintptr(unsafe.Pointer(&v)))
		if r != 0 { // PDH_INVALID_DATA on the first rate sample, or any error
			return 0, false
		}
		return v.DoubleValue, true
	}
	q, okQ := read(d.queue)
	i, okI := read(d.iops)
	l, okL := read(d.latency)
	if !okQ || !okI || !okL {
		return diskSample{}
	}
	return diskSample{QueueLength: q, IOPS: i, LatencyMs: l * 1000, OK: true}
}

// close releases the PDH query. Safe to call more than once.
func (d *diskSampler) close() {
	if d.query != 0 {
		procPdhCloseQuery.Call(d.query)
		d.query = 0
		d.ready = false
	}
}
