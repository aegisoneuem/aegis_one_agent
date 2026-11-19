//go:build !windows

package main

// Non-Windows: Windows Defender is Windows-only. Linux/macOS AV exclusions (if any)
// use their own mechanisms; no-op here.
func applyAgentAVExclusions()  {}
func removeAgentAVExclusions() {}
