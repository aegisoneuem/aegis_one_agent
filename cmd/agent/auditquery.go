package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	"github.com/aegisoneuem/aegis_one_agent/internal/fileops"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// auditQuerySpec is the JSON payload of a signed audit_query/v1 command. It
// carries the filter (embedded auditlog.Query) plus whether to include rotated
// files. There is DELIBERATELY no path field: the handler only ever reads the
// agent's own audit directory, so a remote operator can query the local trace
// but can never turn this into an arbitrary-file read (least privilege, unlike
// the generic file_operation path).
type auditQuerySpec struct {
	Version        int  `json:"version"`
	IncludeRotated bool `json:"include_rotated,omitempty"`
	auditlog.Query      // mode, limit, pattern, since_ms, until_ms, event_types, decisions
}

// handleAuditQueryCmd answers a remote audit-query: it reads the local audit
// trace (audit.log + optionally rotated files) from <SecureDir>/logs, applies
// the signed filter, and returns the matching events (newest-first, size-capped)
// as JSON in ActionStatus.Detail. The audit log holds only metadata — no secrets
// or payload bytes — so returning matched events over the already-authenticated
// channel leaks nothing the server did not stream itself.
func (d *dispatcher) handleAuditQueryCmd(ctx context.Context, _ pb.AgentControlPlaneClient, cmd *pb.Command) {
	var spec auditQuerySpec
	if err := json.Unmarshal(cmd.Payload, &spec); err != nil || spec.Version != 1 {
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionAuditQuery, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, "invalid audit_query/v1 payload"))
		return
	}
	if err := spec.Query.Validate(); err != nil {
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionAuditQuery, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, err.Error()))
		return
	}

	go func() {
		defer recoverLog("audit-query")
		slog.Info("audit_query accepted", "command_id", cmd.CommandId,
			"mode", spec.Mode, "limit", spec.Limit, "pattern", spec.Pattern,
			"include_rotated", spec.IncludeRotated,
			"actor", attributionOrUnknown(cmd.Actor),
			"request_source", attributionOrUnknown(cmd.Source),
			"issued_at", cmd.IssuedAt)
		started := time.Now()

		dir := filepath.Join(d.cfg.SecureDir, "logs")
		events, readErr := auditlog.ReadAll(dir, spec.IncludeRotated)
		matched := auditlog.Filter(events, spec.Query)

		// Serialize matches, and keep the result under the shared manifest cap by
		// dropping the OLDEST matches (the tail of the newest-first slice) until it
		// fits — the operator keeps the most recent events, which is what forensics
		// wants, and the response can never blow the 2 MiB wire budget.
		detail, _ := json.Marshal(matched)
		trimmed := 0
		for len(detail) > fileops.MaxManifestBytes && len(matched) > 0 {
			matched = matched[:len(matched)-1]
			trimmed++
			detail, _ = json.Marshal(matched)
		}

		reason := fmt.Sprintf("audit_query: %d event(s) returned (scanned %d)", len(matched), len(events))
		if trimmed > 0 {
			reason += fmt.Sprintf("; %d older match(es) omitted to fit the size cap", trimmed)
		}
		if readErr != nil {
			// A partial/unreadable rotated file is NOT fatal — we still return the
			// events we could parse, but flag the degradation honestly.
			reason += "; read warning: " + readErr.Error()
		}

		sendStatus(d.outbox, &pb.ActionStatus{
			ActionId: cmd.CommandId, ActionType: actionAuditQuery, DeviceId: d.cfg.DeviceID,
			Phase: pb.ActionPhase_ACTION_PHASE_SUCCESS, Reason: reason, Detail: string(detail),
			StartedAt: started.Unix(), FinishedAt: time.Now().Unix(),
		})
		slog.Info("audit_query finished", "command_id", cmd.CommandId, "returned", len(matched), "scanned", len(events), "trimmed", trimmed)
	}()
}
