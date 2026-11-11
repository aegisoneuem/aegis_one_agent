package auditlog

import (
	"fmt"
	"sync"
	"time"
)

// Guard protects the audit channel from flooding WITHOUT dropping high-value
// events. Only the high-volume benign event (command_received) is subject to
// duplicate-coalescing and a token-bucket rate cap. Every security and outcome
// event — command_rejected, command_completed, audit_degraded, enrollment*,
// cert_renew* — is ALWAYS admitted, so:
//   - a flood can never drown out a command_rejected (an attacker cannot hide
//     their rejected attempts by spamming benign traffic), and
//   - the server's completion-driven logic is never starved of command_completed.
//
// Suppression is itself audited: an "audit_rate_limited" summary rides alongside
// the next admitted event, so the flood is recorded honestly, never hidden.
//
// Guard holds no file or network handle; it is pure in-memory state guarded by a
// mutex, so it is safe for concurrent Admit calls and trivially unit-testable.
type Guard struct {
	mu        sync.Mutex
	now       func() time.Time
	rate      float64 // tokens per second for the guarded event
	burst     float64 // bucket capacity
	tokens    float64
	last      time.Time
	dupWindow time.Duration
	recent    map[string]int64 // key -> last-seen UnixNano, for duplicate coalescing
	dupCount  int64            // duplicates suppressed since the last summary
	rateCount int64            // over-rate events suppressed since the last summary
	sinceMS   int64            // UnixMilli when the current suppression streak began
}

const (
	// DefaultAuditRate is the sustained command_received events/sec before the
	// token bucket starts shedding. High enough that normal operation never
	// trips it; low enough that a runaway loop is bounded.
	DefaultAuditRate = 50
	// DefaultAuditBurst is the bucket capacity (a short burst above the rate is
	// absorbed without suppression).
	DefaultAuditBurst = 100
	// DefaultAuditDupWindow is how close two identical command_received records
	// must be to coalesce (handles reconnect re-delivery storms).
	DefaultAuditDupWindow = 2 * time.Second
	// maxRecentKeys bounds the dedup map so a flood of distinct ids cannot grow
	// it without limit; stale entries are pruned once this is exceeded.
	maxRecentKeys = 4096
)

// NewGuard builds a Guard. Non-positive values fall back to the defaults.
func NewGuard(rate, burst float64, dupWindow time.Duration) *Guard {
	if rate <= 0 {
		rate = DefaultAuditRate
	}
	if burst <= 0 {
		burst = DefaultAuditBurst
	}
	if dupWindow <= 0 {
		dupWindow = DefaultAuditDupWindow
	}
	now := time.Now()
	return &Guard{
		now: time.Now, rate: rate, burst: burst, tokens: burst, last: now,
		dupWindow: dupWindow, recent: make(map[string]int64),
	}
}

// Reconfigure updates the guard's rate/burst/dedup window at runtime (a signed
// agent policy's audit override). A non-positive value leaves that parameter
// unchanged. Safe for concurrent Admit calls; the live token count is clamped to
// a reduced burst so a lowered cap takes effect immediately.
func (g *Guard) Reconfigure(rate, burst float64, dupWindow time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if rate > 0 {
		g.rate = rate
	}
	if burst > 0 {
		g.burst = burst
		if g.tokens > g.burst {
			g.tokens = g.burst
		}
	}
	if dupWindow > 0 {
		g.dupWindow = dupWindow
	}
}

// guarded reports whether an event type is subject to flood control. ONLY the
// high-volume benign command_received is; everything else is always admitted.
func guarded(eventType string) bool {
	return eventType == "command_received"
}

// Admit decides whether ev is written now. When suppression has occurred since
// the last admitted event, it also returns a summary Event to write ALONGSIDE ev
// so the flood is recorded honestly. summary is nil when there is nothing to
// report or when ev itself is being suppressed.
func (g *Guard) Admit(ev Event) (write bool, summary *Event) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !guarded(ev.EventType) {
		// High-value event: always write, and flush any pending suppression note.
		return true, g.flushLocked(ev.DeviceID)
	}

	nowT := g.now()
	nowNS := nowT.UnixNano()

	// Duplicate coalescing: the same command_id+event_type seen again within the
	// window is a re-delivery/retry echo — suppress it (counted, not hidden).
	key := ev.CommandID + "|" + ev.EventType
	if last, ok := g.recent[key]; ok && nowNS-last < int64(g.dupWindow) {
		g.recent[key] = nowNS
		g.markSuppressedLocked(&g.dupCount, nowT)
		return false, nil
	}
	g.recent[key] = nowNS
	g.pruneLocked(nowNS)

	// Token bucket for genuinely novel command_received volume.
	g.refillLocked(nowT)
	if g.tokens >= 1 {
		g.tokens--
		return true, g.flushLocked(ev.DeviceID)
	}
	g.markSuppressedLocked(&g.rateCount, nowT)
	return false, nil
}

func (g *Guard) refillLocked(nowT time.Time) {
	elapsed := nowT.Sub(g.last).Seconds()
	if elapsed <= 0 {
		return
	}
	g.tokens += elapsed * g.rate
	if g.tokens > g.burst {
		g.tokens = g.burst
	}
	g.last = nowT
}

func (g *Guard) markSuppressedLocked(counter *int64, nowT time.Time) {
	if g.dupCount == 0 && g.rateCount == 0 {
		g.sinceMS = nowT.UnixMilli()
	}
	*counter++
}

// flushLocked returns a summary of any suppression since the last flush and
// resets the counters. nil when nothing was suppressed.
func (g *Guard) flushLocked(deviceID string) *Event {
	if g.dupCount == 0 && g.rateCount == 0 {
		return nil
	}
	nowMS := g.now().UnixMilli()
	dup, over := g.dupCount, g.rateCount
	windowMS := nowMS - g.sinceMS
	g.dupCount, g.rateCount, g.sinceMS = 0, 0, 0
	return &Event{
		DeviceID:  deviceID,
		TS:        nowMS,
		EventType: "audit_rate_limited",
		Decision:  "suppressed",
		Reason: fmt.Sprintf("audit flood control suppressed %d duplicate + %d over-rate command_received event(s) over %d ms",
			dup, over, windowMS),
		Details: fmt.Sprintf("duplicates=%d, over_rate=%d, window_ms=%d", dup, over, windowMS),
	}
}

// pruneLocked drops stale dedup keys once the map grows past the cap, so a flood
// of distinct command ids cannot grow memory without bound.
func (g *Guard) pruneLocked(nowNS int64) {
	if len(g.recent) <= maxRecentKeys {
		return
	}
	cutoff := nowNS - int64(g.dupWindow)
	for k, last := range g.recent {
		if last < cutoff {
			delete(g.recent, k)
		}
	}
}
