//go:build !windows

package main

import (
	"log/slog"

	"github.com/aegisoneuem/aegis_one_agent/internal/buffer"
	"github.com/aegisoneuem/aegis_one_agent/internal/config"
)

// runPatchDetection is a no-op on non-Windows platforms for now; Windows patch
// detection lives in detection_windows.go. Linux/macOS patch detection will be
// added later with their native mechanisms.
func runPatchDetection(cfg config.Config, buf buffer.Buffer) {
	slog.Info("patch detection not supported on this OS yet")
}
