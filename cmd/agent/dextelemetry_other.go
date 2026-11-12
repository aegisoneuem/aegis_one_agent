//go:build !windows

package main

// resourceSampler stub off-Windows; the Linux/macOS samplers (/proc/stat,
// sysinfo, host_statistics) land with the cross-platform phase.
type resourceSampler struct{}

func newResourceSampler() *resourceSampler { return &resourceSampler{} }

func (*resourceSampler) sample() (cpuPct float64, cpuOK bool, memUsedPct float64, memOK bool) {
	return 0, false, 0, false
}
