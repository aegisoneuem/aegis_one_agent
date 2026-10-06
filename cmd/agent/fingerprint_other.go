//go:build !windows

package main

// hardwareFingerprint is Windows-specific (SMBIOS/registry). On other platforms
// it returns "" — clone detection is skipped and a persisted id is reused as-is.
func hardwareFingerprint() string { return "" }

// hardwareIDs is Windows-specific; returns empty identifiers elsewhere.
func hardwareIDs() (systemUUID, machineGUID string) { return "", "" }
