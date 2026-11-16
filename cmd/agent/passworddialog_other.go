//go:build !windows

package main

// Non-Windows: no native dialog; callers fall back to the stdin prompt / property.
func promptPasswordDialog(title, prompt string) (string, bool) { return "", false }
