//go:build windows

package main

import "golang.org/x/sys/windows/registry"

// UnprotectProvisioned is a NON-SECRET registry marker (HKLM\SOFTWARE\AegisOne\
// Client) set when an unprotect passcode is configured. It lets the agent
// fail-closed: if the passcode FILE is deleted but this marker is present, the
// service STILL self-protects (and raises unprotect_secret_tampered) — so deleting
// the file hardens the lock instead of removing it. SYSTEM/admin-writable only, not
// a loose file alongside the secret.
const (
	unprotectMarkerRegPath = `SOFTWARE\AegisOne\Client`
	// Innocuous value name (not "UnprotectProvisioned") so it blends with normal
	// config and doesn't jump out to a casual/AI-assisted admin. Obscurity is thin —
	// see .svcstate note; the durable defenses are the watchdog + server re-assert + TPM.
	unprotectMarkerRegValue = "SchemaRevision"
)

func readUnprotectMarker() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, unprotectMarkerRegPath, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(unprotectMarkerRegValue)
	return err == nil && v == 1
}

func writeUnprotectMarker() error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, unprotectMarkerRegPath, registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetDWordValue(unprotectMarkerRegValue, 1)
}

func deleteUnprotectMarker() error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, unprotectMarkerRegPath, registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.DeleteValue(unprotectMarkerRegValue)
}
