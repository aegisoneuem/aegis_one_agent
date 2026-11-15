//go:build windows

package main

import "testing"

// Reads the real power state on whatever box the test runs on. A laptop reports
// Present=true with a 0-100 charge; a desktop/VM reports Present=false. Either is a
// valid pass — we only assert the call succeeds and values are self-consistent.
func TestReadBattery_Real(t *testing.T) {
	b := readBattery()
	if !b.OK {
		t.Fatal("GetSystemPowerStatus call failed")
	}
	if b.Present {
		if b.ChargePct < -1 || b.ChargePct > 100 {
			t.Fatalf("charge out of range: %d", b.ChargePct)
		}
	} else if b.ChargePct != -1 {
		t.Fatalf("no battery but charge=%d (want -1)", b.ChargePct)
	}
	t.Logf("battery: present=%v ac_online=%v charge_pct=%d", b.Present, b.ACOnline, b.ChargePct)
}
