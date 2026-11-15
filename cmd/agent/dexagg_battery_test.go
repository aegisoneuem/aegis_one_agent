package main

import "testing"

// Laptop on battery draining 80→60: present=true, ac=false, charge avg/min/max=70/60/80.
func TestDexAgg_Battery(t *testing.T) {
	var a dexAgg
	a.addCPU(1)
	a.addBattery(batterySample{Present: true, ACOnline: false, ChargePct: 80, OK: true})
	a.addBattery(batterySample{Present: true, ACOnline: false, ChargePct: 60, OK: true})

	r := a.report("dev", 30)
	if !r.BatteryPresent || r.BatteryAcOnline {
		t.Fatalf("present/ac = %v/%v want true/false", r.BatteryPresent, r.BatteryAcOnline)
	}
	if r.BatteryChargePctAvg != 70 || r.BatteryChargePctMin != 60 || r.BatteryChargePctMax != 80 {
		t.Fatalf("charge avg/min/max=%v/%v/%v want 70/60/80", r.BatteryChargePctAvg, r.BatteryChargePctMin, r.BatteryChargePctMax)
	}
}

// Desktop/VM: present=false, no charge samples → charge fields stay 0, present=false.
func TestDexAgg_BatteryAbsent(t *testing.T) {
	var a dexAgg
	a.addCPU(1)
	a.addBattery(batterySample{Present: false, ChargePct: -1, OK: true})
	r := a.report("dev", 30)
	if r.BatteryPresent {
		t.Fatal("present must be false with no battery")
	}
	if r.BatteryChargePctAvg != 0 {
		t.Fatalf("charge avg=%v want 0 (no samples)", r.BatteryChargePctAvg)
	}
}

// No battery sample at all (OK=false) → battery fields untouched (all zero/false).
func TestDexAgg_BatteryNotSampled(t *testing.T) {
	var a dexAgg
	a.addCPU(1)
	a.addBattery(batterySample{OK: false})
	r := a.report("dev", 30)
	if r.BatteryPresent || r.BatteryAcOnline || r.BatteryChargePctAvg != 0 {
		t.Fatalf("expected empty battery fields: %+v", r)
	}
}
