package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// auditLogger is the agent-wide audit sink. Set once at startup via
// initAuditLogger() and used by recordAudit() from the receive loop and
// the enroll / cert-renew paths. nil when the local audit log could not
// be initialized at startup; recordAudit still emits the server-bound
// AuditEvent so the authoritative server-side record stays intact.
var auditLogger *auditlog.Logger

// initAuditLogger opens or creates <SKVC>/logs/audit.log and seeds the
// chain head from any existing file so the chain continues across
// restarts. Called once from main() before runAgent. A failure here is
// logged as a warning — the server-side audit stream still works even
// without a local buffer.
func initAuditLogger(cfg config.Config) {
	dir := filepath.Join(cfg.SecureDir, "logs")
	l, err := auditlog.New(dir)
	if err != nil {
		slog.Warn("audit log init failed; proceeding WITHOUT local audit buffer (server-side audit still active)",
			"dir", dir, "error", err.Error())
		return
	}
	auditLogger = l
	slog.Info("audit log ready", "path", filepath.Join(dir, "audit.log"))
}

// recordAudit writes one audit record to the local rolling buffer AND
// enqueues an AuditEvent on the outbox for streaming to the server. The
// server is the AUTHORITATIVE compliance record — local buffer is
// best-effort (local admin can tamper). Called from the receive loop
// for both accepted and rejected signed commands, and from the enroll /
// cert-renew paths.
//
// On local-write failure, emits an additional "audit_degraded" event so
// the server sees that the endpoint's local buffer is unhealthy. The
// original audit record still reaches the server via the outbox.
func recordAudit(ctx context.Context, outbox chan<- *pb.AgentMessage, deviceID string, cmd *pb.Command, signatureOK bool, decision, reason string) {
	eventType := "command_received"
	if !signatureOK || decision == "rejected" {
		eventType = "command_rejected"
	}
	ev := auditlog.Event{
		DeviceID:    deviceID,
		TS:          time.Now().UnixMilli(),
		EventType:   eventType,
		CommandID:   cmd.CommandId,
		CommandType: cmd.Type,
		Actor:       cmd.Actor,
		Source:      cmd.Source,
		IssuedAt:    cmd.IssuedAt,
		SignatureOK: signatureOK,
		Decision:    decision,
		Reason:      reason,
	}
	writeAuditEvent(ctx, outbox, ev)
}

// recordAuditEvent is the generic entry point for non-command events
// (enrollment success/failure, cert renewal, audit_degraded). The caller
// builds the Event and we handle local append + server emit.
func recordAuditEvent(ctx context.Context, outbox chan<- *pb.AgentMessage, ev auditlog.Event) {
	if ev.TS == 0 {
		ev.TS = time.Now().UnixMilli()
	}
	writeAuditEvent(ctx, outbox, ev)
}

// writeAuditEvent is the common implementation shared by recordAudit and
// recordAuditEvent: local append (if the logger is up), then push a
// pb.AuditEvent onto the outbox. A local write failure emits an
// additional audit_degraded event so the server knows.
func writeAuditEvent(ctx context.Context, outbox chan<- *pb.AgentMessage, ev auditlog.Event) {
	written := ev
	if auditLogger != nil {
		stamped, err := auditLogger.Append(ev)
		if err != nil {
			slog.Error("audit log local append failed — server audit stream still active", "error", err.Error())
			// Emit the original event (without chain hashes since we
			// couldn't anchor it locally) PLUS an audit_degraded marker.
			sendAuditEvent(ctx, outbox, ev)
			deg := auditlog.Event{
				DeviceID:  ev.DeviceID,
				TS:        time.Now().UnixMilli(),
				EventType: "audit_degraded",
				Decision:  "degraded",
				Reason:    err.Error(),
			}
			sendAuditEvent(ctx, outbox, deg)
			return
		}
		written = stamped
	}
	sendAuditEvent(ctx, outbox, written)
}

// sendAuditEvent pushes one audit event onto the outbox. Converts the
// local Event struct into pb.AuditEvent and uses the same best-effort
// non-blocking send pattern the rest of the agent uses. A full outbox
// means we drop the event rather than block the receive loop — the
// server will see the gap via chain-hash discontinuity when the next
// successful event streams.
func sendAuditEvent(ctx context.Context, outbox chan<- *pb.AgentMessage, ev auditlog.Event) {
	msg := &pb.AgentMessage{Payload: &pb.AgentMessage_AuditEvent{AuditEvent: &pb.AuditEvent{
		DeviceId:    ev.DeviceID,
		Ts:          ev.TS,
		EventType:   ev.EventType,
		CommandId:   ev.CommandID,
		CommandType: ev.CommandType,
		Actor:       ev.Actor,
		Source:      ev.Source,
		IssuedAt:    ev.IssuedAt,
		SignatureOk: ev.SignatureOK,
		Decision:    ev.Decision,
		Reason:      ev.Reason,
		PrevHash:    ev.PrevHash,
		LineHash:    ev.LineHash,
	}}}
	send(ctx, outbox, msg)
}
