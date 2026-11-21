//go:build !windows && !linux

package main

// hardwareFingerprint is Windows/Linux-specific. On other platforms (macOS/BSD —
// added later) it returns "" — clone detection is skipped and a persisted id is
// reused as-is.
func hardwareFingerprint() string { return "" }

// hardwareSignals is Windows-specific; returns no signals elsewhere.
func hardwareSignals() []string { return nil }

// hardwareIDs is Windows-specific; returns empty identifiers elsewhere.
func hardwareIDs() (tpmEK, systemUUID, machineGUID string) { return "", "", "" }
