//go:build !windows

package main

import "context"

// Non-Windows: session-0 isolation and the interactive tray helper are Windows
// concepts. Linux/macOS user-messaging will use their own mechanisms (libnotify /
// NSUserNotification) in a later port; no-op here.
func runSessionHelperSupervisor(_ context.Context, _ string) {}

// Non-Windows: no cross-session service-alive beacon needed.
func startServiceAliveBeacon() {}
