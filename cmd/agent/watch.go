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
			// A single install/uninstall touches MANY CBS package keys → many fires.
			// Log once per debounce window (at INFO); the rest just extend the timer
			// (DEBUG) so the log is not spammed.
			if !armed {
				slog.Info("servicing change detected (CBS) — will re-scan after it settles",
					"debounce", servicingRescanDebounce.String())
			} else {
				slog.Debug("servicing change (CBS) — extending debounce")
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(servicingRescanDebounce)
			armed = true
		case <-timer.C:
			if !armed {
				break
			}
			// If the AGENT is mid install/uninstall, its own CBS churn is expected and
			// already reported via ActionStatus — defer the re-scan until it finishes
			// so we don't scan repeatedly during the install. External/manual changes
			// happen while not busy and fire normally.
			if servicingBusy.Load() {
				slog.Debug("servicing re-scan deferred: agent install/uninstall in progress")
				timer.Reset(servicingRescanDebounce)
				break
			}
			armed = false
			slog.Info("servicing settled — triggering compliance re-scan")
			signalDrain(scanTrigger)
		}
	}
}
