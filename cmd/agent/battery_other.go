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

// readBatteryWear is Windows-only (SetupAPI + IOCTL_BATTERY); no-op elsewhere.
func readBatteryWear() (float64, int, bool) { return 0, 0, false }
