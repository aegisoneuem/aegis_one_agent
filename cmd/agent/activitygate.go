package main

import "sync/atomic"

// activityGate is a process-wide counter of in-progress HEAVY operations (patch
// install, software deploy). It lets lightweight background work — notably DEX
// resource sampling — step aside while the agent is doing real work, so the agent
// never piles telemetry load onto a patch/deploy and so DEX CPU/memory readings
// reflect the user's device experience, not the agent's own install activity.
//
// It is a counter, not a boolean, so overlapping or nested heavy ops compose
// correctly: the gate reads Busy until the LAST one leaves. All methods are
// safe for concurrent use.
type activityGate struct {
	n atomic.Int32
}

// heavyWork is the shared gate. Heavy workers Enter/Leave it; the DEX sampler
// reads Busy. Zero value is ready to use (no construction needed).
var heavyWork activityGate

// Enter marks the start of a heavy operation.
func (g *activityGate) Enter() { g.n.Add(1) }

// Leave marks the end of a heavy operation. Pairs with Enter (use via defer).
// It never drops below zero, so a stray Leave can't wedge the gate "free" while
// another op is still running.
func (g *activityGate) Leave() {
	if g.n.Add(-1) < 0 {
		g.n.Store(0)
	}
}

// Busy reports whether at least one heavy operation is in progress.
func (g *activityGate) Busy() bool { return g.n.Load() > 0 }
