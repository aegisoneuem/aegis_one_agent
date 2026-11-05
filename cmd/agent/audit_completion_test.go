package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// drainAudit pulls all AuditEvents from outbox and returns them. Caller
// must have closed the channel (or ensured no more writes).
func drainAudit(ch chan *pb.AgentMessage) []*pb.AuditEvent {
	var out []*pb.AuditEvent
	for msg := range ch {
		if ae := msg.GetAuditEvent(); ae != nil {
			out = append(out, ae)
		}
	}
	return out
}

func withTempAuditLogger(t *testing.T) (dir string, restore func()) {
	t.Helper()
	tmp := t.TempDir()
	l, err := auditlog.New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	saved := auditLogger
	auditLogger = l
	return tmp, func() { auditLogger = saved }
}

// L2.5a — phase mapping: SUCCESS→succeeded, FAILED→failed,
// NOT_APPLICABLE→skipped. Each produces exactly one audit record.
func TestRecordCompletionAudit_PhaseMapping(t *testing.T) {
	_, restore := withTempAuditLogger(t)
	defer restore()

	cases := []struct {
		phase pb.ActionPhase
		want  string
	}{
		{pb.ActionPhase_ACTION_PHASE_SUCCESS, "succeeded"},
		{pb.ActionPhase_ACTION_PHASE_FAILED, "failed"},
		{pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE, "skipped"},
	}
	for _, c := range cases {
		outbox := make(chan *pb.AgentMessage, 4)
		recordCompletionAudit(outbox, &pb.ActionStatus{
			ActionId: "a-" + c.want, ActionType: "install_patch",
			DeviceId: "dev-x", Phase: c.phase, ExitCode: 0,
		})
		close(outbox)
		evs := drainAudit(outbox)
		if len(evs) != 1 {
			t.Errorf("phase %v: got %d events, want 1", c.phase, len(evs))
			continue
		}
		if evs[0].EventType != "command_completed" {
			t.Errorf("phase %v: event_type=%q, want command_completed", c.phase, evs[0].EventType)
		}
		if evs[0].Decision != c.want {
			t.Errorf("phase %v: decision=%q, want %q", c.phase, evs[0].Decision, c.want)
		}
	}
}

// L2.5b — intermediate phases emit NOTHING. Audit-noise control is a
// hard requirement; a chatty audit stream is a broken audit stream.
func TestRecordCompletionAudit_IntermediatePhasesSkipped(t *testing.T) {
	_, restore := withTempAuditLogger(t)
	defer restore()

	intermediate := []pb.ActionPhase{
		pb.ActionPhase_ACTION_PHASE_UNSPECIFIED,
		pb.ActionPhase_ACTION_PHASE_QUEUED,
		pb.ActionPhase_ACTION_PHASE_DOWNLOADING,
		pb.ActionPhase_ACTION_PHASE_VERIFYING,
		pb.ActionPhase_ACTION_PHASE_RUNNING,
	}
	for _, p := range intermediate {
		outbox := make(chan *pb.AgentMessage, 2)
		recordCompletionAudit(outbox, &pb.ActionStatus{
			ActionId: "a-noise", Phase: p,
		})
		close(outbox)
		if evs := drainAudit(outbox); len(evs) != 0 {
			t.Errorf("phase %v produced %d audit events; want 0", p, len(evs))
		}
	}
}

// L2.5c — duration_ms = (finished - started) * 1000 when both set.
// Both zero → duration_ms=0.
func TestRecordCompletionAudit_DurationMS(t *testing.T) {
	_, restore := withTempAuditLogger(t)
	defer restore()

	outbox := make(chan *pb.AgentMessage, 2)
	recordCompletionAudit(outbox, &pb.ActionStatus{
		ActionId: "a-1", Phase: pb.ActionPhase_ACTION_PHASE_SUCCESS,
		ExitCode: 0, StartedAt: 1_700_000_000, FinishedAt: 1_700_000_045,
	})
	close(outbox)
	evs := drainAudit(outbox)
	if len(evs) != 1 {
		t.Fatalf("got %d events", len(evs))
	}
	if !strings.Contains(evs[0].Details, "duration_ms=45000") {
		t.Errorf("Details missing duration_ms=45000: %q", evs[0].Details)
	}
	if !strings.Contains(evs[0].Details, "exit=0") {
		t.Errorf("Details missing exit=0: %q", evs[0].Details)
	}
}

// L2.5d — Reason threaded into Details when non-empty.
func TestRecordCompletionAudit_ReasonInDetails(t *testing.T) {
	_, restore := withTempAuditLogger(t)
	defer restore()

	outbox := make(chan *pb.AgentMessage, 2)
	recordCompletionAudit(outbox, &pb.ActionStatus{
		ActionId: "a-2", Phase: pb.ActionPhase_ACTION_PHASE_FAILED,
		ExitCode: 1603, Reason: "MSI 1603: fatal error during installation",
	})
	close(outbox)
	evs := drainAudit(outbox)
	if len(evs) != 1 {
		t.Fatalf("got %d events", len(evs))
	}
	if !strings.Contains(evs[0].Details, "exit=1603") {
		t.Errorf("Details missing exit=1603: %q", evs[0].Details)
	}
	if !strings.Contains(evs[0].Details, "reason=MSI 1603") {
		t.Errorf("Details missing reason text: %q", evs[0].Details)
	}
	if evs[0].Reason != "MSI 1603: fatal error during installation" {
		t.Errorf("Reason not preserved: %q", evs[0].Reason)
	}
}

// L2.6 — end-to-end: command_received + command_completed pair. The two
// share command_id so the auditor can join them, and both appear in the
// outbox AND on-disk audit.log in order with chain hashes intact.
func TestAudit_ReceivedCompletedPair(t *testing.T) {
	tmp, restore := withTempAuditLogger(t)
	defer restore()

	outbox := make(chan *pb.AgentMessage, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := &pb.Command{CommandId: "server-42", Type: "install_patch",
		Actor: "console:ops@example", Source: "server.console"}
	recordAudit(ctx, outbox, "dev-pair", cmd, true, "accepted", "")

	// Simulate the handler finishing successfully.
	recordCompletionAudit(outbox, &pb.ActionStatus{
		ActionId: "server-42", ActionType: "install_patch",
		DeviceId:  "dev-pair",
		Phase:     pb.ActionPhase_ACTION_PHASE_SUCCESS,
		ExitCode:  0,
		StartedAt: 1_700_000_000, FinishedAt: 1_700_000_012,
	})
	close(outbox)
	evs := drainAudit(outbox)
	if len(evs) != 2 {
		t.Fatalf("got %d events, want 2", len(evs))
	}
	if evs[0].EventType != "command_received" || evs[1].EventType != "command_completed" {
		t.Errorf("order wrong: %q then %q", evs[0].EventType, evs[1].EventType)
	}
	if evs[0].CommandId != evs[1].CommandId {
		t.Errorf("command_id mismatch between received (%q) and completed (%q)",
			evs[0].CommandId, evs[1].CommandId)
	}
	// On-disk audit.log mirrors the stream (2 events).
	disk, err := auditlog.Read(filepath.Join(tmp, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(disk) != 2 {
		t.Errorf("on-disk events = %d, want 2", len(disk))
	}
}

// --- Break tests --------------------------------------------------------

// Break: nil ActionStatus must not panic and must emit nothing.
func TestBreak_Completion_NilStatus(t *testing.T) {
	_, restore := withTempAuditLogger(t)
	defer restore()

	outbox := make(chan *pb.AgentMessage, 2)
	// Should not panic.
	recordCompletionAudit(outbox, nil)
	close(outbox)
	if evs := drainAudit(outbox); len(evs) != 0 {
		t.Errorf("nil status produced %d audit events; want 0", len(evs))
	}
}

// Break: very long reason (>200 bytes) is truncated inside Details but
// preserved in the Reason field — truncation applies to the Details line
// which is meant for scan/greppability, not to the full reason.
func TestBreak_Completion_LongReasonTruncatedInDetails(t *testing.T) {
	_, restore := withTempAuditLogger(t)
	defer restore()

	long := strings.Repeat("A", 5000)
	outbox := make(chan *pb.AgentMessage, 2)
	recordCompletionAudit(outbox, &pb.ActionStatus{
		ActionId: "a-long", Phase: pb.ActionPhase_ACTION_PHASE_FAILED,
		ExitCode: 1, Reason: long,
	})
	close(outbox)
	evs := drainAudit(outbox)
	if len(evs) != 1 {
		t.Fatalf("got %d events", len(evs))
	}
	if len(evs[0].Details) > 400 {
		t.Errorf("Details length=%d, expected truncated under 400", len(evs[0].Details))
	}
	if evs[0].Reason != long {
		t.Errorf("full Reason must be preserved verbatim; len=%d", len(evs[0].Reason))
	}
}

// Break: clock skew or missing timestamps → duration_ms=0 and no crash.
// Covers StartedAt=0, FinishedAt=0, and finished<started.
func TestBreak_Completion_WeirdTimestamps(t *testing.T) {
	_, restore := withTempAuditLogger(t)
	defer restore()

	cases := []struct {
		name  string
		start int64
		end   int64
	}{
		{"both_zero", 0, 0},
		{"only_start", 1_700_000_000, 0},
		{"only_end", 0, 1_700_000_000},
		{"end_before_start", 1_700_000_100, 1_700_000_050},
	}
	for _, c := range cases {
		outbox := make(chan *pb.AgentMessage, 2)
		recordCompletionAudit(outbox, &pb.ActionStatus{
			ActionId: "a-" + c.name, Phase: pb.ActionPhase_ACTION_PHASE_SUCCESS,
			StartedAt: c.start, FinishedAt: c.end,
		})
		close(outbox)
		evs := drainAudit(outbox)
		if len(evs) != 1 {
			t.Errorf("%s: got %d events; want 1", c.name, len(evs))
			continue
		}
		if !strings.Contains(evs[0].Details, "duration_ms=0") {
			t.Errorf("%s: Details should show duration_ms=0, got %q", c.name, evs[0].Details)
		}
	}
}

// Break: concurrent terminal-phase writes from many handlers must not
// corrupt the chain or drop events. Simulates the realistic case where a
// batch of SoftDist tasks finish in parallel.
func TestBreak_Completion_ConcurrentWrites(t *testing.T) {
	tmp, restore := withTempAuditLogger(t)
	defer restore()

	const N = 50
	outbox := make(chan *pb.AgentMessage, N*2)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			recordCompletionAudit(outbox, &pb.ActionStatus{
				ActionId: "a-" + string(rune('A'+i%26)),
				Phase:    pb.ActionPhase_ACTION_PHASE_SUCCESS,
				ExitCode: 0,
			})
		}(i)
	}
	wg.Wait()
	close(outbox)
	evs := drainAudit(outbox)
	if len(evs) != N {
		t.Errorf("concurrent completion: got %d events, want %d", len(evs), N)
	}
	// On-disk mirrors stream (count-only; no chain, no corruption).
	disk, err := auditlog.Read(filepath.Join(tmp, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(disk) != N {
		t.Errorf("on-disk events = %d, want %d", len(disk), N)
	}
}
