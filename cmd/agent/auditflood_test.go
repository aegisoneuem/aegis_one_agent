package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// TestAuditFlood_Integration_DedupAndExempt proves flood control is wired into
// the REAL recordAudit -> writeAuditEvent path (not dead code): a storm of
// identical command_received is coalesced, a command_rejected is NEVER dropped,
// and the suppression is recorded honestly as an audit_rate_limited summary.
func TestAuditFlood_Integration_DedupAndExempt(t *testing.T) {
	saved := auditGuard
	t.Cleanup(func() { auditGuard = saved })
	// Huge token budget so ONLY duplicate-coalescing suppresses (deterministic).
	auditGuard = auditlog.NewGuard(1e9, 1e9, 10*time.Second)

	outbox := make(chan *pb.AgentMessage, 64)
	ctx := context.Background()
	dup := &pb.Command{CommandId: "dup-1", Type: "run_command"}
	for i := 0; i < 10; i++ { // 1 admitted, 9 coalesced
		recordAudit(ctx, outbox, "dev", dup, true, "accepted", "")
	}
	// A command_rejected must always pass AND flush the suppression summary.
	recordAudit(ctx, outbox, "dev", &pb.Command{CommandId: "rej-1", Type: "x"}, false, "rejected", "bad signature")
	close(outbox)

	var received, rejected, rateLimited int
	var summaryReason string
	for m := range outbox {
		ae := m.GetAuditEvent()
		if ae == nil {
			continue
		}
		switch ae.EventType {
		case "command_received":
			received++
		case "command_rejected":
			rejected++
		case "audit_rate_limited":
			rateLimited++
			summaryReason = ae.Reason
		}
	}
	if received != 1 {
		t.Fatalf("expected 1 command_received (9 coalesced), got %d", received)
	}
	if rejected != 1 {
		t.Fatalf("command_rejected must never be suppressed, got %d", rejected)
	}
	if rateLimited != 1 {
		t.Fatalf("expected exactly one audit_rate_limited summary, got %d", rateLimited)
	}
	if !strings.Contains(summaryReason, "9 duplicate") {
		t.Fatalf("summary should report 9 duplicates, got %q", summaryReason)
	}
}

// With the guard disabled (nil) auditing is fail-open — every event passes.
func TestAuditFlood_Integration_FailOpenWhenGuardNil(t *testing.T) {
	saved := auditGuard
	t.Cleanup(func() { auditGuard = saved })
	auditGuard = nil

	outbox := make(chan *pb.AgentMessage, 32)
	cmd := &pb.Command{CommandId: "dup-2", Type: "run_command"}
	for i := 0; i < 5; i++ {
		recordAudit(context.Background(), outbox, "dev", cmd, true, "accepted", "")
	}
	close(outbox)
	n := 0
	for m := range outbox {
		if m.GetAuditEvent() != nil {
			n++
		}
	}
	if n != 5 {
		t.Fatalf("guard nil must fail-open (all 5 events), got %d", n)
	}
}
