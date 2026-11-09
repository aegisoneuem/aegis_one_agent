package auditlog

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// clockGuard builds a Guard with a controllable clock for deterministic tests.
func clockGuard(rate, burst float64, dup time.Duration) (*Guard, *time.Time) {
	g := NewGuard(rate, burst, dup)
	now := time.Unix(1_700_000_000, 0)
	g.now = func() time.Time { return now }
	g.last = now
	return g, &now
}

func rcv(id string) Event { return Event{DeviceID: "d", EventType: "command_received", CommandID: id} }

// Every non-command_received event type is always admitted, no matter the volume.
func TestGuard_ExemptEventsAlwaysAdmitted(t *testing.T) {
	g, _ := clockGuard(1, 1, time.Second) // deliberately tiny budget
	exempt := []string{"command_rejected", "command_completed", "audit_degraded", "audit_rate_limited", "enrollment", "cert_renew"}
	for _, et := range exempt {
		for i := 0; i < 50; i++ {
			w, _ := g.Admit(Event{DeviceID: "d", EventType: et, CommandID: "x"})
			if !w {
				t.Fatalf("%s must always be admitted (iter %d)", et, i)
			}
		}
	}
}

// A flood of command_received must NEVER cause a command_rejected to be dropped.
func TestGuard_Security_FloodDoesNotDropRejected(t *testing.T) {
	g, _ := clockGuard(5, 5, time.Second)
	rejectedAdmitted := 0
	for i := 0; i < 500; i++ {
		g.Admit(rcv(fmt.Sprintf("flood-%d", i))) // most of these get shed
		if i%50 == 0 {
			w, _ := g.Admit(Event{DeviceID: "d", EventType: "command_rejected", CommandID: fmt.Sprintf("r-%d", i), Decision: "rejected"})
			if !w {
				t.Fatalf("command_rejected #%d was suppressed under flood — security regression", i)
			}
			rejectedAdmitted++
		}
	}
	if rejectedAdmitted == 0 {
		t.Fatal("test did not exercise any rejected events")
	}
}

// Identical command_received within the window coalesces; after the window it is
// admitted again.
func TestGuard_DuplicateCoalescing(t *testing.T) {
	g, now := clockGuard(1000, 1000, 2*time.Second) // big token budget to isolate dedup
	if w, _ := g.Admit(rcv("cmd-1")); !w {
		t.Fatal("first occurrence must be admitted")
	}
	if w, _ := g.Admit(rcv("cmd-1")); w {
		t.Fatal("duplicate within window must be suppressed")
	}
	*now = now.Add(3 * time.Second) // past the dedup window
	if w, _ := g.Admit(rcv("cmd-1")); !w {
		t.Fatal("same id after the window must be admitted again")
	}
}

// The token bucket sheds sustained novel volume and recovers as time passes.
func TestGuard_TokenBucketShedsAndRefills(t *testing.T) {
	g, now := clockGuard(10, 5, time.Hour) // dup window huge so distinct ids only hit the bucket
	admitted := 0
	for i := 0; i < 5; i++ {
		if w, _ := g.Admit(rcv(fmt.Sprintf("a-%d", i))); w {
			admitted++
		}
	}
	if admitted != 5 {
		t.Fatalf("burst of 5 should all pass, got %d", admitted)
	}
	if w, _ := g.Admit(rcv("over")); w {
		t.Fatal("6th within the same instant must be shed (bucket empty)")
	}
	*now = now.Add(time.Second) // +10 tokens at rate=10/s (capped at burst 5)
	if w, _ := g.Admit(rcv("after-refill")); !w {
		t.Fatal("after refill an event must be admitted again")
	}
}

// Suppression is recorded honestly: after events are shed, the next admitted
// event carries an audit_rate_limited summary with the counts, and the counters
// reset afterward.
func TestGuard_SuppressionEmitsHonestSummary(t *testing.T) {
	g, now := clockGuard(1, 1, 500*time.Millisecond)
	g.Admit(rcv("seed")) // consumes the only token
	// Two duplicates of a NEW id? No — duplicates need same id. Shed by rate:
	g.Admit(rcv("x1")) // bucket empty -> over-rate suppressed
	g.Admit(rcv("x2")) // over-rate suppressed
	*now = now.Add(2 * time.Second) // refill + past dup window
	// A high-value event flushes the summary alongside itself.
	w, summary := g.Admit(Event{DeviceID: "d", EventType: "command_completed", CommandID: "done"})
	if !w {
		t.Fatal("command_completed must be admitted")
	}
	if summary == nil {
		t.Fatal("expected an audit_rate_limited summary after suppression")
	}
	if summary.EventType != "audit_rate_limited" || summary.Decision != "suppressed" {
		t.Fatalf("bad summary: %+v", summary)
	}
	// Counters reset: a second admit with no new suppression yields no summary.
	if _, s2 := g.Admit(Event{DeviceID: "d", EventType: "command_completed", CommandID: "done2"}); s2 != nil {
		t.Fatalf("summary should have reset, got %+v", s2)
	}
}

// Break: a flood of DISTINCT ids must not grow the dedup map without bound.
func TestGuard_Break_DistinctIdsPruned(t *testing.T) {
	g, now := clockGuard(1e9, 1e9, 10*time.Millisecond) // never rate-limit; tiny dup window
	for i := 0; i < maxRecentKeys*2; i++ {
		g.Admit(rcv(fmt.Sprintf("id-%d", i)))
		if i%1000 == 0 {
			*now = now.Add(time.Second) // age entries past the window so prune can drop them
		}
	}
	g.mu.Lock()
	n := len(g.recent)
	g.mu.Unlock()
	if n > maxRecentKeys {
		t.Fatalf("dedup map not pruned: %d entries", n)
	}
}

// Concurrency: Admit is mutex-guarded and must not race.
func TestGuard_ConcurrentAdmit(t *testing.T) {
	g := NewGuard(100, 100, time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				g.Admit(rcv(fmt.Sprintf("c-%d-%d", n, j)))
				g.Admit(Event{DeviceID: "d", EventType: "command_rejected", CommandID: "r"})
			}
		}(i)
	}
	wg.Wait()
}
