//go:build !windows

package main

// Non-Windows stub: Wi-Fi reading uses the Windows wlanapi. readWifi yields
// OK=false so callers omit Wi-Fi metrics on other platforms.
type wifiSample struct {
	Present   bool
	Connected bool
	SignalPct int
	SSID      string
	OK        bool
}

func readWifi() wifiSample { return wifiSample{} }
