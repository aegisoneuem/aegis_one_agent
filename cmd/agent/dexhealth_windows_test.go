//go:build windows

package main

import "testing"

func TestCollectDexHealth(t *testing.T) {
	dh := collectDexHealth()
	if dh == nil {
		t.Fatal("nil dex health")
	}
	if dh.WindowDays != dexWindowDays {
		t.Fatalf("window_days=%d want %d", dh.WindowDays, dexWindowDays)
	}
	if dh.UnexpectedShutdowns < 0 || dh.Bugchecks < 0 || dh.DirtyPowerEvents < 0 || dh.BootDurationMs < 0 || dh.AppCrashes < 0 || dh.AppHangs < 0 {
		t.Fatalf("values must be non-negative: %+v", dh)
	}
	t.Logf("dex-health: unexpected_shutdowns=%d bugchecks=%d dirty_power=%d app_crashes=%d app_hangs=%d boot_ms=%d",
		dh.UnexpectedShutdowns, dh.Bugchecks, dh.DirtyPowerEvents, dh.AppCrashes, dh.AppHangs, dh.BootDurationMs)
	for _, f := range dh.AppFaults {
		t.Logf("  fault: %s crashes=%d hangs=%d", f.Name, f.Crashes, f.Hangs)
	}
}

// countSystemEvents must return a non-negative count without hanging on a real
// System channel. Event 6005 ("event log service started") fires on every boot.
func TestCountSystemEvents_RealChannel(t *testing.T) {
	n := countWinEvents("System", "", 6005, int64(365)*86400*1000)
	if n < 0 {
		t.Fatalf("negative count: %d", n)
	}
	t.Logf("System 6005 count over 1y: %d", n)
}
