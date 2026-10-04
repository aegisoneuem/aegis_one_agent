package main

import (
	"fmt"
	"runtime/debug"

	"github.com/aegisoneuem/aegis_one_agent/internal/logging"
)

// safeRun runs fn, recovering from a panic so one module's crash cannot take
// down the whole agent process (principle: per-module isolation). A recovered
// panic is logged CRITICAL with its stack; callers decide whether to continue
// (loops) or tear down (per-connection goroutines). Returns true if it panicked.
func safeRun(module string, fn func()) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			logging.Critical("module panic recovered — agent stays up",
				"module", module, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		}
	}()
	fn()
	return
}

// recoverLog is a deferred panic handler for a goroutine: it recovers and logs
// CRITICAL so the goroutine's panic does not crash the process. Place it as
// `defer recoverLog("name")` at the top of the goroutine; any other deferred
// teardown (e.g. cancel) should be deferred first so it still runs.
func recoverLog(module string) {
	if r := recover(); r != nil {
		logging.Critical("module panic recovered — agent stays up",
			"module", module, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
	}
}
