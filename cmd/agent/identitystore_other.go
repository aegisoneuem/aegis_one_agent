//go:build !windows

package main

// Non-Windows platforms have no durable machine store yet (the Windows build uses
// the HKLM registry). SKVC\identity.json is the only store, so these are no-ops;
// stable identity across reinstall then relies on server-side reconciliation.
func restoreIdentityIfMissing(string) {}
func mirrorIdentityToDurable(string)  {}
