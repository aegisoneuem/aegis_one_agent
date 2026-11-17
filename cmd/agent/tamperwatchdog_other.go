//go:build !windows

package main

// Non-Windows: service start-type re-enable is Windows-specific; no-op elsewhere.
func reEnableServiceStart() bool { return false }

// Non-Windows: service re-registration is Windows-specific; no-op elsewhere.
func restoreServiceIfMissing() bool { return false }
