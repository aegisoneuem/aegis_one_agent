//go:build !windows

package main

// Non-Windows: the provisioned marker is Windows-registry-backed; no-op elsewhere
// (Linux/macOS ports add their own protected marker store).
func readUnprotectMarker() bool    { return false }
func writeUnprotectMarker() error  { return nil }
func deleteUnprotectMarker() error { return nil }
