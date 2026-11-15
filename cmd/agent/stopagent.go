package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// Token-authorized agent stop. When the service is self-protected it no longer
// accepts the SCM STOP control, so `sc stop` / services.msc fail. The ONLY legit
// stop path is a stop_agent command carrying a valid server-signed unprotect token
// (console/server path). A matching local-CLI path can post the same token later.

var (
	agentStopMu   sync.Mutex
	agentStopFunc func()
)

func setAgentStopFunc(f func()) {
	agentStopMu.Lock()
	agentStopFunc = f
	agentStopMu.Unlock()
}

// requestAgentStop cancels the root context, unwinding the agent to a clean stop.
func requestAgentStop() {
	agentStopMu.Lock()
	f := agentStopFunc
	agentStopMu.Unlock()
	if f != nil {
		f()
	}
}

// handleStopAgentCmd stops the agent ONLY if the command carries a valid unprotect
// token (payload = the token string). Always token-gated — this is the authorized
// stop path regardless of the tamper-protect switch.
func (d *dispatcher) handleStopAgentCmd(ctx context.Context, _ pb.AgentControlPlaneClient, cmd *pb.Command) {
	secret := strings.TrimSpace(string(cmd.Payload))
	ok, reason := authorizeUnprotect(secret, d.cfg.DeviceID)
	if !ok {
		slog.Warn("tamper-protect: stop_agent REFUSED — invalid token", "command_id", cmd.CommandId, "reason", reason)
		recordAuditEvent(ctx, d.outbox, auditlog.Event{
			DeviceID: d.cfg.DeviceID, EventType: "agent_stop_refused", CommandID: cmd.CommandId,
			Actor: cmd.Actor, Source: cmd.Source, Decision: "refused", Reason: reason,
		})
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionStopAgent, d.cfg.DeviceID, "",
			pb.ActionPhase_ACTION_PHASE_FAILED, "stop refused: "+reason))
		return
	}
	slog.Warn("tamper-protect: stop_agent AUTHORIZED — shutting down", "command_id", cmd.CommandId)
	recordAuditEvent(ctx, d.outbox, auditlog.Event{
		DeviceID: d.cfg.DeviceID, EventType: "agent_stop_authorized", CommandID: cmd.CommandId,
		Actor: cmd.Actor, Source: cmd.Source, Decision: "authorized", Reason: "valid unprotect token",
	})
	sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionStopAgent, d.cfg.DeviceID, "",
		pb.ActionPhase_ACTION_PHASE_SUCCESS, "stop authorized — agent shutting down"))
	// Let the SUCCESS status + audit flush to the server before the stream tears
	// down, then cancel the root context.
	go func() {
		time.Sleep(750 * time.Millisecond)
		requestAgentStop()
	}()
}
