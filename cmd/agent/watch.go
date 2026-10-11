package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/patch"
)

// servicingRescanDebounce is the quiet period after the last CBS change before a
// re-scan fires. A single install/uninstall touches many package keys and takes
// time; waiting for things to settle coalesces the burst into ONE heavy scan.
const servicingRescanDebounce = 60 * time.Second

// watchServicingForRescan bridges CBS servicing changes to a debounced compliance
// re-scan: it watches the servicing store (event-driven, ~zero cost) and, after
// changes settle, pokes scanTrigger so the agent re-scans "what is missing" right
// after a local install/uninstall — without running the heavy scan on a timer.
func watchServicingForRescan(ctx context.Context, scanTrigger chan<- struct{}) {
	defer recoverLog("servicing-watch")

	fire := make(chan struct{}, 1)
	go func() {
		defer recoverLog("servicing-watch-raw")
		patch.WatchServicing(ctx, fire)
	}()

	timer := time.NewTimer(servicingRescanDebounce)
	if !timer.Stop() {
		<-timer.C
	}
	armed := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-fire:
			slog.Info("servicing change detected (CBS) — scheduling a compliance re-scan after it settles",
				"debounce", servicingRescanDebounce.String())
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(servicingRescanDebounce)
			armed = true
		case <-timer.C:
			if armed {
				armed = false
				slog.Info("servicing settled — triggering compliance re-scan")
				signalDrain(scanTrigger)
			}
		}
	}
}
