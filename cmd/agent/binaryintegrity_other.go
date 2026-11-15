//go:build !windows

package main

// Non-Windows baseline store: no-op (empty baseline → the check records silently
// and never alerts). The Linux/macOS agent ports will add their own baseline store.
func platformReadBinaryBaseline() (string, string)          { return "", "" }
func platformWriteBinaryBaseline(version, hash string) error { return nil }
