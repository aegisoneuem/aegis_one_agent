package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// fakeIndicator records Show/Hide calls so tests can assert the visible-indicator
// lifecycle (addendum #2) without any UI.
type fakeIndicator struct {
	shows int
	hides int
	lastOp string
}

func (f *fakeIndicator) Show(operator string, _ time.Time) { f.shows++; f.lastOp = operator }
func (f *fakeIndicator) Hide()                             { f.hides++ }

// newTestRC builds a manager with a fixed clock, a fake indicator, and a present
// interactive user, so the state machine is tested deterministically.
func newTestRC(now time.Time) (*rcManager, *fakeIndicator) {
	ind := &fakeIndicator{}
	m := &rcManager{
		now:             func() time.Time { return now },
		indicator:       ind,
		interactiveUser: func() (string, bool) { return "DESKTOP\\alice", true },
	}
	return m, ind
}

// validToken returns a well-formed token expiring 15 minutes after base.
func validToken(base time.Time) string {
	return fmt.Sprintf("v1.%d.nonce", base.Add(15*time.Minute).Unix())
}

func startSpec(base time.Time) rcSpec {
	return rcSpec{Version: 1, Action: "start", SessionID: "s1", Operator: "admin-x", Mode: "unattended", MaintToken: validToken(base)}
}

func TestRC_StartActivatesSessionAndIndicator(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m, ind := newTestRC(now)

	res := m.handle(startSpec(now))
	if res.Phase != pb.ActionPhase_ACTION_PHASE_SUCCESS || res.Decision != "started" {
		t.Fatalf("start: got phase=%v decision=%s reason=%q", res.Phase, res.Decision, res.Reason)
	}
	if ind.shows != 1 {
		t.Fatalf("indicator Show not called exactly once: %d", ind.shows)
	}
	if m.current == nil || m.current.ID != "s1" {
		t.Fatalf("session not active")
	}
	if m.current.InteractiveUser != "DESKTOP\\alice" || !m.current.UserPresent {
		t.Fatalf("interactive user not captured: %+v", m.current)
	}
	// snapshot must name the controller.
	var snap rcSnapshot
	if err := json.Unmarshal([]byte(res.Detail), &snap); err != nil {
		t.Fatalf("snapshot not JSON: %v", err)
	}
	if !snap.Active || snap.Controller != "admin-x" {
		t.Fatalf("snapshot wrong: %+v", snap)
	}
	// agent-based session must be SYSTEM/admin-capable (service-desk needs admin).
	if snap.ExecContext != "system" {
		t.Fatalf("agent-based session exec_context must be system, got %q", snap.ExecContext)
	}
}

func TestRC_StartRejectsExpiredToken(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m, ind := newTestRC(now)
	spec := startSpec(now)
	spec.MaintToken = fmt.Sprintf("v1.%d.nonce", now.Add(-time.Minute).Unix()) // already expired

	res := m.handle(spec)
	if res.Phase != pb.ActionPhase_ACTION_PHASE_FAILED || res.Decision != "denied" {
		t.Fatalf("expected denied, got %+v", res)
	}
	if m.current != nil || ind.shows != 0 {
		t.Fatalf("session must not start on expired token")
	}
}

func TestRC_StartRejectsOverlongTokenTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m, _ := newTestRC(now)
	spec := startSpec(now)
	spec.MaintToken = fmt.Sprintf("v1.%d.nonce", now.Add(2*time.Hour).Unix()) // beyond rcMaintTokenMaxTTL

	if res := m.handle(spec); res.Decision != "denied" {
		t.Fatalf("expected denied for over-long TTL, got %+v", res)
	}
}

func TestRC_StartRejectsMalformedToken(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for _, tok := range []string{"", "garbage", "v1.notanumber.nonce", "v2.123.nonce", fmt.Sprintf("v1.%d.", now.Add(time.Minute).Unix())} {
		m, _ := newTestRC(now)
		spec := startSpec(now)
		spec.MaintToken = tok
		if res := m.handle(spec); res.Decision != "denied" {
			t.Fatalf("token %q: expected denied, got %+v", tok, res)
		}
	}
}

func TestRC_StartRequiresSessionAndOperator(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m, _ := newTestRC(now)
	spec := startSpec(now)
	spec.Operator = ""
	if res := m.handle(spec); res.Decision != "denied" {
		t.Fatalf("expected denied for missing operator, got %+v", res)
	}
}

func TestRC_ConcurrentSessionRejectedButSameIsIdempotent(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m, _ := newTestRC(now)
	m.handle(startSpec(now))

	// different session id -> rejected
	other := startSpec(now)
	other.SessionID = "s2"
	if res := m.handle(other); res.Decision != "denied" {
		t.Fatalf("concurrent session must be denied, got %+v", res)
	}
	// same session id -> idempotent success
	if res := m.handle(startSpec(now)); res.Decision != "idempotent" {
		t.Fatalf("same session restart must be idempotent, got %+v", res)
	}
}

func TestRC_JoinAddsViewerThenPassControl(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m, _ := newTestRC(now)
	m.handle(startSpec(now))

	join := rcSpec{Version: 1, Action: "join", SessionID: "s1", Operator: "admin-y", MaintToken: validToken(now)}
	if res := m.handle(join); res.Decision != "joined" {
		t.Fatalf("join failed: %+v", res)
	}
	if len(m.current.Participants) != 2 {
		t.Fatalf("expected 2 participants, got %d", len(m.current.Participants))
	}
	// joiner must be a viewer (one-controller model)
	for _, p := range m.current.Participants {
		if p.Operator == "admin-y" && p.Controller {
			t.Fatalf("joiner must not be controller")
		}
	}
	// duplicate join is idempotent
	if res := m.handle(join); res.Decision != "idempotent" {
		t.Fatalf("duplicate join must be idempotent, got %+v", res)
	}
	// pass control to admin-y
	pass := rcSpec{Version: 1, Action: "pass_control", SessionID: "s1", Target: "admin-y"}
	if res := m.handle(pass); res.Decision != "passed" {
		t.Fatalf("pass_control failed: %+v", res)
	}
	controllers := 0
	for _, p := range m.current.Participants {
		if p.Controller {
			controllers++
			if p.Operator != "admin-y" {
				t.Fatalf("control did not move to admin-y")
			}
		}
	}
	if controllers != 1 {
		t.Fatalf("exactly one controller expected, got %d", controllers)
	}
}

func TestRC_JoinAndPassControlNegativeCases(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m, _ := newTestRC(now)
	m.handle(startSpec(now))

	// join wrong session id
	if res := m.handle(rcSpec{Version: 1, Action: "join", SessionID: "nope", Operator: "z", MaintToken: validToken(now)}); res.Decision != "denied" {
		t.Fatalf("join wrong session must be denied, got %+v", res)
	}
	// pass_control to non-participant
	if res := m.handle(rcSpec{Version: 1, Action: "pass_control", SessionID: "s1", Target: "ghost"}); res.Decision != "denied" {
		t.Fatalf("pass to non-participant must be denied, got %+v", res)
	}
	// pass_control with no target
	if res := m.handle(rcSpec{Version: 1, Action: "pass_control", SessionID: "s1"}); res.Decision != "denied" {
		t.Fatalf("pass with no target must be denied, got %+v", res)
	}
}

func TestRC_StopEndsSessionAndHidesIndicator(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m, ind := newTestRC(now)
	m.handle(startSpec(now))

	// stop with wrong id -> denied, session stays
	if res := m.handle(rcSpec{Version: 1, Action: "stop", SessionID: "wrong"}); res.Decision != "denied" {
		t.Fatalf("stop wrong id must be denied, got %+v", res)
	}
	if m.current == nil {
		t.Fatalf("session must survive a mismatched stop")
	}
	// correct stop
	if res := m.handle(rcSpec{Version: 1, Action: "stop", SessionID: "s1"}); res.Decision != "ended" {
		t.Fatalf("stop failed: %+v", res)
	}
	if m.current != nil || ind.hides != 1 {
		t.Fatalf("session not torn down / indicator not hidden")
	}
	// stop when idle -> noop / NOT_APPLICABLE
	res := m.handle(rcSpec{Version: 1, Action: "stop", SessionID: "s1"})
	if res.Phase != pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE || res.Decision != "noop" {
		t.Fatalf("idle stop must be noop, got %+v", res)
	}
}

func TestRC_StatusExpiresActiveSessionFailClosed(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m, ind := newTestRC(now)
	m.handle(startSpec(now)) // token expiry = now+15m

	// advance past expiry and query status
	m.now = func() time.Time { return now.Add(30 * time.Minute) }
	res := m.handle(rcSpec{Version: 1, Action: "status"})
	if res.Decision != "expired" {
		t.Fatalf("status past expiry must report expired, got %+v", res)
	}
	if m.current != nil || ind.hides != 1 {
		t.Fatalf("expired session must be torn down (fail-closed)")
	}
}

func TestRC_StatusIdle(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m, _ := newTestRC(now)
	res := m.handle(rcSpec{Version: 1, Action: "status"})
	if res.Decision != "idle" {
		t.Fatalf("idle status wrong: %+v", res)
	}
}

func TestRC_UnknownActionDenied(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m, _ := newTestRC(now)
	if res := m.handle(rcSpec{Version: 1, Action: "hijack", SessionID: "s1"}); res.Decision != "denied" {
		t.Fatalf("unknown action must be denied, got %+v", res)
	}
}
