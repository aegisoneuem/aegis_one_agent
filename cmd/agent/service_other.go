//go:build !windows

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// runControlCommand is a no-op on non-Windows (service control is Windows-only).
func runControlCommand(cmd string) bool { return false }

// refreshServiceStopAcceptance is a no-op on non-Windows.
func refreshServiceStopAcceptance(selfProtect bool) {}

// runAgentEntry runs in the console on non-Windows, stopping gracefully on signal.
func runAgentEntry() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runProcess(ctx)
}
