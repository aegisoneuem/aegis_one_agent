//go:build !windows

package hwinventory

import "runtime"

// Collect on non-Windows returns a minimal snapshot. Linux / macOS
// collectors will be added later with their own native syscalls (sysconf,
// /proc, sysctl). Windows is the Phase-1 target.
func Collect() (*Inventory, error) {
	return &Inventory{
		CPULogicalCores: int32(runtime.NumCPU()),
	}, nil
}
