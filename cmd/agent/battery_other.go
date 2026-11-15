//go:build !windows

package main

// Non-Windows stub. readBattery yields OK=false so callers omit battery metrics.
type batterySample struct {
	Present   bool
	ACOnline  bool
	ChargePct int
	OK        bool
}

func readBattery() batterySample { return batterySample{} }
