package main

import "testing"

// Connected laptop, signal 80→90: present/connected true, signal avg/min/max=85/80/90, ssid kept.
func TestDexAgg_Wifi(t *testing.T) {
	var a dexAgg
	a.addCPU(1)
	a.addWifi(wifiSample{Present: true, Connected: true, SignalPct: 80, SSID: "Corp-WiFi", OK: true})
	a.addWifi(wifiSample{Present: true, Connected: true, SignalPct: 90, SSID: "Corp-WiFi", OK: true})

	r := a.report("dev", 30)
	if !r.WifiPresent || !r.WifiConnected {
		t.Fatalf("present/connected=%v/%v want true/true", r.WifiPresent, r.WifiConnected)
	}
	if r.WifiSignalPctAvg != 85 || r.WifiSignalPctMin != 80 || r.WifiSignalPctMax != 90 {
		t.Fatalf("signal avg/min/max=%v/%v/%v want 85/80/90", r.WifiSignalPctAvg, r.WifiSignalPctMin, r.WifiSignalPctMax)
	}
	if r.WifiSsid != "Corp-WiFi" {
		t.Fatalf("ssid=%q want Corp-WiFi", r.WifiSsid)
	}
}

// Wi-Fi adapter present but not associated → connected=false, no signal samples.
func TestDexAgg_WifiPresentNotConnected(t *testing.T) {
	var a dexAgg
	a.addCPU(1)
	a.addWifi(wifiSample{Present: true, Connected: false, OK: true})
	r := a.report("dev", 30)
	if !r.WifiPresent || r.WifiConnected {
		t.Fatalf("present/connected=%v/%v want true/false", r.WifiPresent, r.WifiConnected)
	}
	if r.WifiSignalPctAvg != 0 {
		t.Fatalf("signal avg=%v want 0 (not associated)", r.WifiSignalPctAvg)
	}
}

// No Wi-Fi adapter (desktop/VM) → present=false, all wifi fields empty.
func TestDexAgg_WifiAbsent(t *testing.T) {
	var a dexAgg
	a.addCPU(1)
	a.addWifi(wifiSample{Present: false, OK: true})
	r := a.report("dev", 30)
	if r.WifiPresent || r.WifiSignalPctAvg != 0 || r.WifiSsid != "" {
		t.Fatalf("expected empty wifi fields: %+v", r)
	}
}
