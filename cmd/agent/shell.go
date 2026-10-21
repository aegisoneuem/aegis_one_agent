package main

import (
	"context"
	"log/slog"
	"time"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

// Shell session defaults (overridable per command via OpenShellSpec).
const (
	shellDefaultTTL         = 60   // seconds — signed open_shell honored within this window
	shellDefaultIdleTimeout = 600  // seconds — 10 min
	shellDefaultMaxDuration = 3600 // seconds — 1 hr
)

// handleOpenShellCmd is the entry for a signed "open_shell" command. It validates
// the spec (TTL, non-empty session_id), ACKs with ActionStatus RUNNING, then dials
// ShellSession in the background: that stream carries stdin/stdout live and never
// blocks the receiver. Every session writes a local transcript the agent uploads
// on close for audit (BladeLogic/compliance baseline — a remote shell with no
// audit trail is a non-starter).
func (d *dispatcher) handleOpenShellCmd(ctx context.Context, client pb.AgentControlPlaneClient, cmd *pb.Command) {
	var spec pb.OpenShellSpec
	if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
		slog.Error("open_shell: invalid payload", "command_id", cmd.CommandId, "error", err.Error())
		send(ctx, d.outbox, buildCommandResultMessage(cmd.CommandId, "failed", "invalid open_shell payload"))
		return
	}
	if spec.SessionId == "" {
		send(ctx, d.outbox, actionStatusMsg(simpleActionStatus(cmd.CommandId, actionOpenShell, d.cfg.DeviceID, "",
			pb.ActionPhase_ACTION_PHASE_FAILED, "open_shell: session_id required")))
		return
	}
	ttl := int64(spec.TtlSeconds)
	if ttl <= 0 {
		ttl = shellDefaultTTL
	}
	// Signed commands can take time to reach the agent on reconnects; refuse one
	// older than its TTL so an attacker cannot replay an old open_shell window.
	if spec.IssuedAt > 0 && time.Now().Unix()-spec.IssuedAt > ttl {
		slog.Warn("open_shell: refused — command expired", "command_id", cmd.CommandId,
			"issued_at", spec.IssuedAt, "ttl", ttl)
		send(ctx, d.outbox, actionStatusMsg(simpleActionStatus(cmd.CommandId, actionOpenShell, d.cfg.DeviceID, "",
			pb.ActionPhase_ACTION_PHASE_FAILED, "open_shell expired (outside TTL)")))
		return
	}

	slog.Info("open_shell: accepted", "command_id", cmd.CommandId, "session_id_prefix", shellIDPrefix(spec.SessionId))
	send(ctx, d.outbox, actionStatusMsg(simpleActionStatus(cmd.CommandId, actionOpenShell, d.cfg.DeviceID, "",
		pb.ActionPhase_ACTION_PHASE_RUNNING, "shell session opening")))

	go func() {
		defer recoverLog("shell-session")
		runShellSession(ctx, client, d.outbox, d.cfg.DeviceID, d.cfg.SecureDir, cmd.CommandId, &spec)
	}()
}

// shellIDPrefix returns the first 8 chars of a session_id — enough to correlate
// logs without printing the full secret in every line.
func shellIDPrefix(id string) string {
	if len(id) > 8 {
		return id[:8] + "…"
	}
	return id
}
