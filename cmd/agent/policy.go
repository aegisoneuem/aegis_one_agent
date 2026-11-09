package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	"github.com/aegisoneuem/aegis_one_agent/internal/policy"
	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
	"github.com/aegisoneuem/aegis_one_proto/commandsign"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// activePolicy is the agent's current signed policy (nil = no policy = no extra
// restriction). Guarded by policyMu for the occasional set_policy update vs the
// frequent CommandAllowed reads (A2).
var (
	policyMu     sync.RWMutex
	activePolicy *policy.Policy
	// policyKeys is the sealer for the anti-rollback floor, set once at startup.
	policyKeys secure.KeyProvider
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

// policyAllowsCommand reports whether a signed command type may run under the
// active policy. set_policy is ALWAYS allowed — a policy can never lock the
// endpoint out of receiving a newer policy. Everything else obeys the policy's
// module allow/deny lists (deny wins; a non-empty allow is a strict allow-list).
func policyAllowsCommand(cmdType string) bool {
	if cmdType == actionSetPolicy {
		return true
	}
	return getActivePolicy().CommandAllowed(cmdType)
}

// loadStoredPolicy reads and RE-VERIFIES the persisted policy at startup, with
// TAMPER DETECTION against the sealed anti-rollback floor. It must run after the
// verify key, audit logger, outbox and key provider are ready (so tamper alerts
// can be recorded + streamed) and before enforceFIPS (so a policy FIPS floor is
// honored). Detected tamper classes:
//   - DELETED  : policy file missing but the floor shows one was applied before
//   - MODIFIED : signature invalid (a local admin edited the file)
//   - ROLLBACK : a validly-signed but OLDER policy (issued_at < floor)
//   - CORRUPT  : unparseable
// Each is refused (no policy applied), AUDITED (policy_tampered — streamed, and
// buffered when offline), and on repeat raises the policy_lockdown "agent lock"
// alert. A valid, non-rolled-back policy is applied and clears the tamper streak.
func loadStoredPolicy(secureDir string, keys secure.KeyProvider, outbox chan<- *pb.AgentMessage, deviceID string) {
	policyKeys = keys
	floor := loadPolicyFloor(keys, secureDir)

	data, err := os.ReadFile(policyStorePath(secureDir))
	if err != nil {
		if floor.MinIssuedAt > 0 {
			recordPolicyTamper(secureDir, keys, outbox, deviceID, &floor, "deleted",
				fmt.Sprintf("policy file missing but a policy (epoch %d) was previously applied", floor.MinIssuedAt))
		}
		return // genuinely no policy yet
	}
	var sp storedPolicy
	if err := json.Unmarshal(data, &sp); err != nil {
		recordPolicyTamper(secureDir, keys, outbox, deviceID, &floor, "corrupt", "stored policy file is not valid JSON")
		return
	}
	if !commandsign.Verify(cmdVerifyKey, sp.Type, sp.CommandID, sp.Payload, sp.Signature) {
		recordPolicyTamper(secureDir, keys, outbox, deviceID, &floor, "modified", "stored policy signature is INVALID (edited or wrong key)")
		return
	}
	p, err := policy.Parse(sp.Payload)
	if err != nil {
		recordPolicyTamper(secureDir, keys, outbox, deviceID, &floor, "corrupt", "stored policy failed validation: "+err.Error())
		return
	}
	if p.IssuedAt < floor.MinIssuedAt {
		recordPolicyTamper(secureDir, keys, outbox, deviceID, &floor, "rollback",
			fmt.Sprintf("policy issued_at %d is older than the anti-rollback floor %d", p.IssuedAt, floor.MinIssuedAt))
		return
	}

	// Valid and not a rollback: apply, raise the floor, clear the tamper streak.
	policyMu.Lock()
	activePolicy = p
	policyMu.Unlock()
	floor.MinIssuedAt = p.IssuedAt
	floor.TamperCount = 0
	_ = savePolicyFloor(keys, secureDir, floor)
	slog.Info("policy: loaded agent policy", "summary", p.Summary())
	applyPolicy(p)
}

// recordPolicyTamper bumps the tamper counter, logs + AUDITS the tamper (streamed
// to the console, buffered when offline), and raises the policy_lockdown alert on
// repeated abuse. It never applies a policy — a tampered/rolled-back policy is
// refused; the server re-asserts the correct one on the next check-in.
func recordPolicyTamper(secureDir string, keys secure.KeyProvider, outbox chan<- *pb.AgentMessage, deviceID string, floor *policyFloor, kind, reason string) {
	floor.TamperCount++
	floor.LastTamper = time.Now().Unix()
	_ = savePolicyFloor(keys, secureDir, *floor)

	slog.Error("policy: TAMPER detected", "kind", kind, "reason", reason, "tamper_count", floor.TamperCount)
	recordAuditEvent(context.Background(), outbox, auditlog.Event{
		DeviceID:  deviceID,
		EventType: "policy_tampered",
		Decision:  "tampered",
		Reason:    kind + ": " + reason,
		Details:   fmt.Sprintf("kind=%s, tamper_count=%d", kind, floor.TamperCount),
	})
	if floor.TamperCount >= policyLockdownThreshold {
		slog.Error("policy: LOCKDOWN — repeated policy tampering", "tamper_count", floor.TamperCount)
		recordAuditEvent(context.Background(), outbox, auditlog.Event{
			DeviceID:  deviceID,
			EventType: "policy_lockdown",
			Decision:  "locked",
			Reason:    fmt.Sprintf("repeated policy tampering detected (count=%d) — agent integrity at risk; server policy re-assertion required", floor.TamperCount),
			Details:   fmt.Sprintf("tamper_count=%d, threshold=%d, last_tamper=%d", floor.TamperCount, policyLockdownThreshold, floor.LastTamper),
		})
	}
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
	// Anti-rollback: never downgrade below the in-memory active policy OR the
	// persisted sealed floor (the floor survives a restart/file-deletion, so a
	// replayed OLD signed policy is refused even on a fresh boot).
	floor := loadPolicyFloor(policyKeys, d.cfg.SecureDir)
	if !p.Supersedes(getActivePolicy()) || p.IssuedAt < floor.MinIssuedAt {
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionSetPolicy, d.cfg.DeviceID, "",
			pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE, "policy not newer than the active policy/floor (anti-rollback)"))
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
	// Raise the floor + clear the tamper streak — a legit newer policy is a
	// healthy signal that overrides prior tamper counts.
	floor.MinIssuedAt = p.IssuedAt
	floor.TamperCount = 0
	_ = savePolicyFloor(policyKeys, d.cfg.SecureDir, floor)
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
