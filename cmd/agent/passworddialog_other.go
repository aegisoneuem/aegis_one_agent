//go:build !windows

package main

// Non-Windows: no native dialog; callers fall back to the stdin prompt / property.
func promptPasswordDialog(title, prompt string, red bool) (string, bool) { return "", false }
func hideConsoleWindow()                                                  {}
