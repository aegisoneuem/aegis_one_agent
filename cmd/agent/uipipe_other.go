//go:build !windows

package main

import "context"

// Non-Windows: the tray UI pipe is Windows-specific; no-op.
func runUINotifyServer(_ context.Context) {}

func showUserNotification(_, _ string) int { return 0 }
