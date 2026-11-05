package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

// TestRecordAudit_EmitsAcceptedAndRejected is the integration test the
// feature-quality rules demand: run the actual recordAudit path end-to-
// end (local append + outbox emit) with a mix of accepted and rejected
// commands and verify BOTH the on-disk audit.log AND the outbox stream
// carry matching records in order.
func TestRecordAudit_EmitsAcceptedAndRejected(t *testing.T) {
	// Point the package-level auditLogger at a temp dir.
	tmp := t.TempDir()
	l, err := auditlog.New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	saved := auditLogger
	auditLogger = l
	defer func() { auditLogger = saved }()

	outbox := make(chan *pb.AgentMessage, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Simulate 3 accepted + 1 rejected + 1 accepted (audit MUST capture all 5).
	cmds := []struct {
		id    string
		typ   string
		sigOK bool
		dec   string
	}{
		{"nsh-1", "run_command", true, "accepted"},
		{"nsh-2", "file_operation", true, "accepted"},
		{"nsh-3", "run_command", false, "rejected"},
		{"nsh-4", "install_patch", true, "accepted"},
		{"server-5", "software_deploy", true, "accepted"},
	}
	for _, c := range cmds {
		reason := ""
		if !c.sigOK {
			reason = "signature verification failed"
		}
		recordAudit(ctx, outbox, "dev-test", &pb.Command{
			CommandId: c.id, Type: c.typ, Actor: "nsh:test@localhost", Source: "mockserver.nsh",
		}, c.sigOK, c.dec, reason)
	}

	// Drain outbox.
	close(outbox)
	var events []*pb.AuditEvent
	for msg := range outbox {
		if ae := msg.GetAuditEvent(); ae != nil {
			events = append(events, ae)
		}
	}
	if len(events) != len(cmds) {
		t.Errorf("outbox events = %d, want %d", len(events), len(cmds))
	}
	// Order preserved + decision + signature_ok match.
	for i, got := range events {
		if got.CommandId != cmds[i].id {
			t.Errorf("event %d: cmd_id=%q, want %q", i, got.CommandId, cmds[i].id)
		}
		if got.SignatureOk != cmds[i].sigOK {
			t.Errorf("event %d: sig_ok=%v, want %v", i, got.SignatureOk, cmds[i].sigOK)
		}
		if got.Decision != cmds[i].dec {
			t.Errorf("event %d: decision=%q, want %q", i, got.Decision, cmds[i].dec)
		}
	}
	// On-disk audit.log mirrors the stream — read it back and assert count.
	disk, err := auditlog.Read(filepath.Join(tmp, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(disk) != len(cmds) {
		t.Errorf("on-disk events = %d, want %d", len(disk), len(cmds))
	}
}

// L1.9 integration — end-to-end: build a realistic signed command with
// a parsable payload, call recordAudit, and assert the audit record
// carries the payload-type-specific Details field. Covers the full
// production path (not just the summary helper in isolation).
func TestRecordAudit_IncludesPayloadDetails(t *testing.T) {
	tmp := t.TempDir()
	l, err := auditlog.New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	saved := auditLogger
	auditLogger = l
	defer func() { auditLogger = saved }()

	outbox := make(chan *pb.AgentMessage, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	spec := &pb.RunCommandSpec{Shell: "powershell", Command: "Get-Process"}
	payload, _ := proto.Marshal(spec)
	cmd := &pb.Command{CommandId: "nsh-xyz", Type: "run_command", Payload: payload,
		Actor: "nsh:tester@localhost", Source: "mockserver.nsh"}
	recordAudit(ctx, outbox, "dev-integ", cmd, true, "accepted", "")
	close(outbox)

	var ev *pb.AuditEvent
	for msg := range outbox {
		if ae := msg.GetAuditEvent(); ae != nil {
			ev = ae
			break
		}
	}
	if ev == nil {
		t.Fatal("no AuditEvent reached the outbox")
	}
	if !strings.Contains(ev.Details, "shell=powershell") {
		t.Errorf("Details missing shell: %q", ev.Details)
	}
	if !strings.Contains(ev.Details, "command=Get-Process") {
		t.Errorf("Details missing command text: %q", ev.Details)
	}
}

// L1.9 integration — rejected-command path must NOT compute details
// (hostile payload could be malformed; we refuse to parse it).
func TestRecordAudit_RejectedSkipsDetails(t *testing.T) {
	tmp := t.TempDir()
	l, _ := auditlog.New(tmp)
	saved := auditLogger
	auditLogger = l
	defer func() { auditLogger = saved }()

	outbox := make(chan *pb.AgentMessage, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	cmd := &pb.Command{CommandId: "bad-1", Type: "run_command", Payload: []byte{0xde, 0xad}}
	recordAudit(ctx, outbox, "dev-x", cmd, false, "rejected", "bad signature")
	close(outbox)

	for msg := range outbox {
		if ae := msg.GetAuditEvent(); ae != nil {
			if ae.Details != "" {
				t.Errorf("rejected command carried Details=%q; must be empty", ae.Details)
			}
		}
	}
}

// TestRecordAudit_NoLoggerFallsBackToServerStream — when the local
// auditlog failed to initialize (e.g. disk full at boot), the agent
// should STILL stream the AuditEvent to the server so the authoritative
// record is preserved.
func TestRecordAudit_NoLoggerFallsBackToServerStream(t *testing.T) {
	saved := auditLogger
	auditLogger = nil
	defer func() { auditLogger = saved }()

	outbox := make(chan *pb.AgentMessage, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	recordAudit(ctx, outbox, "dev-x", &pb.Command{CommandId: "c1", Type: "run_command"}, true, "accepted", "")
	close(outbox)
	var n int
	for msg := range outbox {
		if msg.GetAuditEvent() != nil {
			n++
		}
	}
	if n != 1 {
		t.Errorf("outbox events = %d, want 1 (server stream must work without local logger)", n)
	}
}
