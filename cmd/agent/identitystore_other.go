//go:build !windows && !linux

package main

// macOS/BSD have no durable machine store yet (Windows = HKLM registry, Linux =
// /var/lib/aegisone). The data-dir identity.json is the only store, so these are
// no-ops; stable identity across reinstall then relies on server-side reconciliation.
func restoreIdentityIfMissing(string) {}
func mirrorIdentityToDurable(string)  {}
