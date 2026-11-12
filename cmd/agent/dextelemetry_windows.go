//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Native DEX resource sampler — CPU via GetSystemTimes, physical memory load via
// GlobalMemoryStatusEx. No PowerShell, no WMI/COM, so sampling does not inflate
// the process.
var (
	dexKernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes       = dexKernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = dexKernel32.NewProc("GlobalMemoryStatusEx")
)

// memoryStatusEx mirrors MEMORYSTATUSEX. dwMemoryLoad is the percent of physical
// memory in use (0-100).
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

// resourceSampler holds the previous CPU time reading so a delta (and therefore
// a utilization %) can be computed between samples.
type resourceSampler struct {
	prevIdle, prevKernel, prevUser uint64
	havePrev                       bool
}

func newResourceSampler() *resourceSampler { return &resourceSampler{} }

func fileTimeToU64(ft windows.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}

// sample reads instantaneous memory load and the CPU utilization since the
// previous sample. memOK is set when memory was read; cpuOK is false on the
// first call (no delta yet) or on error.
func (s *resourceSampler) sample() (cpuPct float64, cpuOK bool, memUsedPct float64, memOK bool) {
	m := memoryStatusEx{length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); r != 0 {
		memUsedPct = float64(m.memoryLoad)
		memOK = true
	}

	var idle, kernel, user windows.Filetime
	if r, _, _ := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user))); r != 0 {
		idU, keU, usU := fileTimeToU64(idle), fileTimeToU64(kernel), fileTimeToU64(user)
		if s.havePrev {
			idleD := idU - s.prevIdle
			total := (keU - s.prevKernel) + (usU - s.prevUser) // kernel time INCLUDES idle
			if total > 0 {
				cpuPct = float64(total-idleD) / float64(total) * 100
				if cpuPct < 0 {
					cpuPct = 0
				} else if cpuPct > 100 {
					cpuPct = 100
				}
				cpuOK = true
			}
		}
		s.prevIdle, s.prevKernel, s.prevUser = idU, keU, usU
		s.havePrev = true
	}
	return
}
