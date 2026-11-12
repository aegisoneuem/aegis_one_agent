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
	if dh.UnexpectedShutdowns < 0 || dh.Bugchecks < 0 || dh.DirtyPowerEvents < 0 {
		t.Fatalf("counts must be non-negative: %+v", dh)
	}
	t.Logf("dex-health: unexpected_shutdowns=%d bugchecks=%d dirty_power=%d",
		dh.UnexpectedShutdowns, dh.Bugchecks, dh.DirtyPowerEvents)
}

// countSystemEvents must return a non-negative count without hanging on a real
// System channel. Event 6005 ("event log service started") fires on every boot.
func TestCountSystemEvents_RealChannel(t *testing.T) {
	n := countSystemEvents(6005, int64(365)*86400*1000)
	if n < 0 {
		t.Fatalf("negative count: %d", n)
	}
	t.Logf("System 6005 count over 1y: %d", n)
}
