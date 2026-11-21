//go:build !windows && !linux

package hwinventory

import "runtime"

// Collect on macOS/BSD returns a minimal snapshot (Windows and Linux have full native
// collectors). A macOS sysctl collector lands with the macOS port.
func Collect() (*Inventory, error) {
	return &Inventory{
		CPULogicalCores: int32(runtime.NumCPU()),
	}, nil
}
