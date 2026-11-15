//go:build windows

package main

import "testing"

// Reads real Wi-Fi state. A laptop reports Present=true (and, if associated,
// Connected=true with a 0-100 signal and an SSID); a desktop/VM reports
// Present=false. All are valid — assert only self-consistency.
func TestReadWifi_Real(t *testing.T) {
	w := readWifi()
	if !w.OK {
		t.Skip("WLAN service unavailable in this environment")
	}
	if w.Connected {
		if !w.Present {
			t.Fatal("Connected implies Present")
		}
		if w.SignalPct < 0 || w.SignalPct > 100 {
			t.Fatalf("signal out of range: %d", w.SignalPct)
		}
	}
	t.Logf("wifi: present=%v connected=%v signal=%d%% ssid=%q", w.Present, w.Connected, w.SignalPct, w.SSID)
}
