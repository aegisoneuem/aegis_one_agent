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

// watchServicingForRescan bridges CBS servicing changes to a debounced refresh:
// it watches the servicing store (event-driven, ~zero cost) and, after changes
// settle, pokes deviceStateTrigger (Layer 1 recollects the full installed-KB set
// so the server re-diffs promptly after a local install/uninstall) and, when the
// optional Layer 4 cab scan is enabled, scanTrigger too. Either trigger is best-
// effort (coalescing channels); a nil scanTrigger is skipped.
func watchServicingForRescan(ctx context.Context, scanTrigger chan<- struct{}, deviceStateTrigger chan<- struct{}) {
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
			slog.Info("servicing settled — refreshing installed-KB state (Layer 1)")
			signalDrain(deviceStateTrigger)
			if scanTrigger != nil {
				signalDrain(scanTrigger)
			}
		}
	}
}
