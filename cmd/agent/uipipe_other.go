//go:build !windows

package main

import (
	"context"

	"github.com/aegisoneuem/aegis_one_agent/internal/uimsg"
)

// Non-Windows: the tray UI pipe is Windows-specific; no-op.
func runUINotifyServer(_ context.Context) {}

func deliverUIMessage(_ uimsg.Message) int { return 0 }
