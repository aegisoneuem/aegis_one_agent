package main

import (
	"context"
	"fmt"
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

// initAuditLogger opens or creates <SKVC>/logs/audit.log as an
// append-only local trace for on-endpoint L1 forensics. Called once
// from main() before runAgent. A failure here is logged as a warning —
// the server-side audit stream is authoritative and still works even
// without a local file.
func initAuditLogger(cfg config.Config) {
	dir := filepath.Join(cfg.SecureDir, "logs")
	l, err := auditlog.New(dir)
	if err != nil {
		slog.Warn("audit log init failed; proceeding WITHOUT local audit trace (server-side audit still active)",
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
	// Details: per-command-type payload summary. Only computed for
	// accepted commands — a rejected command may have a payload the
	// agent could not trust / unmarshal. For rejected events, keeping
	// Details empty prevents a malformed hostile payload from being
	// echoed into the audit record.
	details := ""
	if signatureOK {
		details = payloadSummary(cmd)
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
		Details:     details,
	}
	writeAuditEvent(ctx, outbox, ev)
}

// recordCompletionAudit emits a "command_completed" audit event when a
// signed command reaches a TERMINAL ActionStatus phase (SUCCESS /
// FAILED / NOT_APPLICABLE). Linked to the earlier "command_received"
// event by command_id so an auditor can see BOTH halves of the action
// — dispatch AND outcome — not just "something was received".
//
// Non-terminal phases (QUEUED, DOWNLOADING, VERIFYING, RUNNING) do not
// emit — intermediate transitions are too noisy for audit and the
// server team's analysis layer can reconstruct timing from
// start/finish timestamps on the ActionStatus stream.
//
// Decision vocabulary:
//   SUCCESS         → decision=succeeded
//   FAILED          → decision=failed
//   NOT_APPLICABLE  → decision=skipped
//
// Details field carries the operational outcome summary:
//   exit=<N>, duration_ms=<N>, reason=<truncated>
func recordCompletionAudit(outbox chan<- *pb.AgentMessage, st *pb.ActionStatus) {
	if st == nil || !isTerminalActionPhase(st.Phase) {
		return
	}
	decision := "succeeded"
	switch st.Phase {
	case pb.ActionPhase_ACTION_PHASE_FAILED:
		decision = "failed"
	case pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE:
		decision = "skipped"
	}
	// Prefer agent-measured wall clock (started_at + finished_at). When
	// either is unset, duration = 0 which the auditor reads as "unknown".
	var durationMS int64
	if st.StartedAt > 0 && st.FinishedAt > 0 && st.FinishedAt >= st.StartedAt {
		durationMS = (st.FinishedAt - st.StartedAt) * 1000
	}
	reasonForDetails := st.Reason
	if len(reasonForDetails) > 200 {
		reasonForDetails = reasonForDetails[:200] + "…"
	}
	details := fmt.Sprintf("exit=%d, duration_ms=%d", st.ExitCode, durationMS)
	if reasonForDetails != "" {
		details += ", reason=" + reasonForDetails
	}
	ev := auditlog.Event{
		DeviceID:    st.DeviceId,
		TS:          time.Now().UnixMilli(),
		EventType:   "command_completed",
		CommandID:   st.ActionId,
		CommandType: st.ActionType,
		SignatureOK: true, // completion only follows an already-verified command
		Decision:    decision,
		Reason:      st.Reason,
		Details:     details,
	}
	// context.Background: the status itself was just blocked onto the
	// outbox so one more blocking enqueue for the audit event is in the
	// same risk class.
	writeAuditEvent(context.Background(), outbox, ev)
}

// isTerminalActionPhase returns true when the phase represents the END
// of an action's lifecycle. Intermediate phases (QUEUED / DOWNLOADING /
// VERIFYING / RUNNING) are not audited as completions.
func isTerminalActionPhase(phase pb.ActionPhase) bool {
	switch phase {
	case pb.ActionPhase_ACTION_PHASE_SUCCESS,
		pb.ActionPhase_ACTION_PHASE_FAILED,
		pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE:
		return true
	}
	return false
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
// non-blocking send pattern the rest of the agent uses.
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
		Details:     ev.Details,
	}}}
	send(ctx, outbox, msg)
}
