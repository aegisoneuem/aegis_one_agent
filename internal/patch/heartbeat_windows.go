//go:build windows

package patch

import (
	"log/slog"
	"time"
)

// heartbeatInterval controls how often a long-running blocking operation
// (e.g. the synchronous WUA Search/Download/Install COM calls) emits a
// "still working" line, so operators can tell the agent is alive rather than
// hung. It is deliberately coarse to avoid log spam.
var heartbeatInterval = 10 * time.Second

// startHeartbeat begins logging periodic progress for a blocking operation and
// returns a stop function. Call the returned function (typically via defer or
// immediately after the blocking call) to end the heartbeat; it also logs the
// total elapsed time for that operation.
//
// Usage:
//
//	stop := startHeartbeat("WUA catalog search")
//	result, err := oleutil.CallMethod(searcher, "Search", query)
//	stop()
func startHeartbeat(op string) func() {
	done := make(chan struct{})
	start := time.Now()

	go func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				slog.Info("still working",
					"op", op,
					"elapsed", time.Since(start).Round(time.Second).String())
			}
		}
	}()

	return func() {
		close(done)
		slog.Debug("operation finished",
			"op", op,
			"elapsed", time.Since(start).Round(time.Second).String())
	}
}
