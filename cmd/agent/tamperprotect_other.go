//go:build !windows

package main

// Non-Windows stub. Linux/macOS install-dir hardening (chmod/chown root-only) lands
// with those agent ports.
func hardenDirACL(dir string) error { return nil }
