package auditlog

import (
	"testing"
	"time"
)

// Reconfigure retunes the guard at runtime (the A3 policy audit override). A
// non-positive parameter is left unchanged, and a reduced burst clamps the live
// token count so the lower cap bites immediately.
func TestGuard_Reconfigure(t *testing.T) {
	g, _ := clockGuard(50, 100, 2*time.Second)

	g.Reconfigure(0, 0, 0) // no-op
	if g.rate != 50 || g.burst != 100 || g.dupWindow != 2*time.Second {
		t.Fatalf("no-op reconfigure changed state: rate=%v burst=%v dup=%v", g.rate, g.burst, g.dupWindow)
	}

	g.Reconfigure(5, 0, 0)
	if g.rate != 5 || g.burst != 100 {
		t.Fatalf("rate-only reconfigure wrong: rate=%v burst=%v", g.rate, g.burst)
	}

	g.Reconfigure(0, 7, 3*time.Second)
	if g.rate != 5 || g.burst != 7 || g.dupWindow != 3*time.Second {
		t.Fatalf("burst/dup reconfigure wrong: rate=%v burst=%v dup=%v", g.rate, g.burst, g.dupWindow)
	}

	g.tokens = 100
	g.Reconfigure(0, 2, 0)
	if g.tokens != 2 {
		t.Fatalf("tokens not clamped to reduced burst: %v", g.tokens)
	}
}
