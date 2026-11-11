//go:build !windows

package main

// detectInteractiveUser is a stub on non-Windows platforms. Remote Control is a
// Windows-first feature (DXGI capture + SendInput land in RC-1/RC-2); the Linux
// and macOS implementations (X11/Wayland, CoreGraphics) come with the
// cross-platform phase. Returning ("", false) keeps the session plumbing building
// and testable everywhere.
func detectInteractiveUser() (string, bool) { return "", false }
