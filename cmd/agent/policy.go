package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	"github.com/aegisoneuem/aegis_one_agent/internal/policy"
	"github.com/aegisoneuem/aegis_one_proto/commandsign"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// activePolicy is the agent's current signed policy (nil = no policy = no extra
// restriction). Guarded by policyMu for the occasional set_policy update vs the
// frequent CommandAllowed reads (A2).
var (
	policyMu     sync.RWMutex
	activePolicy *policy.Policy
)

// storedPolicy is the signed set_policy envelope persisted to disk so the policy
// can be RE-VERIFIED against the server's public key at startup — a tampered
// policy file is rejected (defense-in-depth beyond file permissions).
type storedPolicy struct {
	Type      string          `json:"type"`
	CommandID string          `json:"command_id"`
	Payload   json.RawMessage `json:"payload"`   // the policy document JSON
	Signature []byte          `json:"signature"` // Ed25519 over (type,id,payload)
}

func policyStorePath(secureDir string) string {
	return filepath.Join(secureDir, "policy.json")
}

// getActivePolicy returns the current policy under the read lock.
func getActivePolicy() *policy.Policy {
	policyMu.RLock()
	defer policyMu.RUnlock()
	return activePolicy
}

// policyRequiresFIPS reports whether the active policy mandates FIPS.
func policyRequiresFIPS() bool {
	p := getActivePolicy()
	return p != nil && p.SecurityFloor.RequireFIPS
}

// loadStoredPolicy reads and RE-VERIFIES the persisted policy at startup. It must
// run AFTER loadCommandVerifyKey (needs the key) and BEFORE enforceFIPS (so a
// policy-mandated FIPS floor is honored fail-closed). A missing file is normal;
// an unreadable/invalid/tampered file is logged and ignored (no policy applied).
func loadStoredPolicy(secureDir string) {
	data, err := os.ReadFile(policyStorePath(secureDir))
	if err != nil {
		return // no policy yet
	}
	var sp storedPolicy
	if err := json.Unmarshal(data, &sp); err != nil {
		slog.Error("policy: stored policy file is corrupt — ignoring", "error", err.Error())
		return
	}
	if !commandsign.Verify(cmdVerifyKey, sp.Type, sp.CommandID, sp.Payload, sp.Signature) {
		slog.Error("policy: stored policy SIGNATURE INVALID — ignoring (tampered or wrong key)")
		return
	}
	p, err := policy.Parse(sp.Payload)
	if err != nil {
		slog.Error("policy: stored policy invalid — ignoring", "error", err.Error())
		return
	}
	policyMu.Lock()
	activePolicy = p
	policyMu.Unlock()
	slog.Info("policy: loaded agent policy", "summary", p.Summary())
	applyPolicy(p)
}

// applyPolicy enacts the parts of a policy that take effect immediately. In A1
// that is the security floor: a FIPS requirement that cannot be satisfied at
// runtime (the Go FIPS module is a start-time choice) is surfaced loudly and
// enforced fail-closed on the next restart via enforceFIPS. Module (A2) and
// behavior (A3) enforcement hook in here later.
func applyPolicy(p *policy.Policy) {
	if p == nil {
		return
	}
	if p.SecurityFloor.RequireFIPS && !fipsActive() {
		slog.Warn("policy: FIPS is REQUIRED by policy but the agent is not in FIPS mode — install --fips (or GODEBUG=fips140=on) and restart; it is enforced fail-closed at next startup")
	}
}

// handleSetPolicyCmd applies a signed set_policy command: parse + validate the
// policy, reject a non-newer one (anti-rollback), persist the signed envelope,
// make it active, apply it, and record a policy_applied audit event. The command
// itself was already Ed25519-verified by the receive loop before dispatch.
func (d *dispatcher) handleSetPolicyCmd(ctx context.Context, _ pb.AgentControlPlaneClient, cmd *pb.Command) {
	p, err := policy.Parse(cmd.Payload)
	if err != nil {
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionSetPolicy, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, err.Error()))
		return
	}
	// Anti-rollback: never downgrade to an older/equal signed policy (replay).
	if !p.Supersedes(getActivePolicy()) {
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionSetPolicy, d.cfg.DeviceID, "",
			pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE, "policy not newer than the active policy (anti-rollback)"))
		return
	}
	sp := storedPolicy{Type: cmd.Type, CommandID: cmd.CommandId, Payload: append(json.RawMessage(nil), cmd.Payload...), Signature: cmd.Signature}
	if err := writePolicyStore(d.cfg.SecureDir, sp); err != nil {
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionSetPolicy, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, "persist policy failed: "+err.Error()))
		return
	}
	policyMu.Lock()
	activePolicy = p
	policyMu.Unlock()
	applyPolicy(p)

	recordAuditEvent(ctx, d.outbox, auditlog.Event{
		DeviceID:  d.cfg.DeviceID,
		EventType: "policy_applied",
		CommandID: cmd.CommandId,
		Actor:     cmd.Actor,
		Source:    cmd.Source,
		Decision:  "applied",
		Reason:    "signed agent policy applied",
		Details:   p.Summary(),
	})
	slog.Info("policy: applied new agent policy", "command_id", cmd.CommandId, "summary", p.Summary())
	sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionSetPolicy, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_SUCCESS, "policy applied: "+p.Summary()))
}

// writePolicyStore persists the signed envelope atomically (temp + rename), 0600.
func writePolicyStore(secureDir string, sp storedPolicy) error {
	raw, err := json.Marshal(sp)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(secureDir, 0o700); err != nil {
		return err
	}
	path := policyStorePath(secureDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
