package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	"github.com/aegisoneuem/aegis_one_agent/internal/policy"
	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
	"github.com/aegisoneuem/aegis_one_proto/commandsign"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

func resetPolicy(t *testing.T) {
	t.Helper()
	saved := activePolicy
	savedKeys := policyKeys
	t.Cleanup(func() {
		policyMu.Lock()
		activePolicy = saved
		policyMu.Unlock()
		policyKeys = savedKeys
	})
	policyMu.Lock()
	activePolicy = nil
	policySafeMode = false
	policyMu.Unlock()
	policyKeys = nil
}

// countEvents scans an outbox for audit events of the given type.
func countEvents(out chan *pb.AgentMessage, eventType string) int {
	n := 0
	for len(out) > 0 {
		if ae := (<-out).GetAuditEvent(); ae != nil && ae.EventType == eventType {
			n++
		}
	}
	return n
}

func policyCmd(t *testing.T, p policy.Policy) *pb.Command {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return &pb.Command{CommandId: "pol-1", Type: "set_policy", Payload: raw}
}

func policyDispatcher(t *testing.T) (*dispatcher, chan *pb.AgentMessage, string) {
	t.Helper()
	secure := t.TempDir()
	out := make(chan *pb.AgentMessage, 8)
	return &dispatcher{cfg: config.Config{SecureDir: secure, DeviceID: "dev-1"}, outbox: out}, out, secure
}

func TestSetPolicy_Integration_AppliesStoresAudits(t *testing.T) {
	resetPolicy(t)
	d, out, secure := policyDispatcher(t)
	p := policy.Policy{Version: 1, PolicyID: "p-100", IssuedAt: 100, Modules: policy.Modules{Deny: []string{"run_command"}}}
	d.handleSetPolicyCmd(context.Background(), nil, policyCmd(t, p))

	// Terminal SUCCESS + a policy_applied audit event.
	var okStatus, applied bool
	for len(out) > 0 {
		m := <-out
		if st := m.GetActionStatus(); st != nil && st.ActionType == actionSetPolicy && st.Phase == pb.ActionPhase_ACTION_PHASE_SUCCESS {
			okStatus = true
		}
		if ae := m.GetAuditEvent(); ae != nil && ae.EventType == "policy_applied" {
			applied = true
		}
	}
	if !okStatus {
		t.Fatal("expected SUCCESS status")
	}
	if !applied {
		t.Fatal("expected a policy_applied audit event")
	}
	// Active policy updated + enforces the deny.
	if getActivePolicy() == nil || getActivePolicy().CommandAllowed("run_command") {
		t.Fatal("policy not active / deny not in force")
	}
	// Persisted to disk.
	if _, err := os.Stat(filepath.Join(secure, "policy.json")); err != nil {
		t.Fatalf("policy not persisted: %v", err)
	}
}

func TestSetPolicy_AntiRollback(t *testing.T) {
	resetPolicy(t)
	d, out, _ := policyDispatcher(t)
	d.handleSetPolicyCmd(context.Background(), nil, policyCmd(t, policy.Policy{Version: 1, PolicyID: "new", IssuedAt: 200}))
	drain(out)
	// An OLDER policy must be refused (anti-rollback) and the active one kept.
	d.handleSetPolicyCmd(context.Background(), nil, policyCmd(t, policy.Policy{Version: 1, PolicyID: "old", IssuedAt: 100}))
	var naSeen bool
	for len(out) > 0 {
		if st := (<-out).GetActionStatus(); st != nil && st.Phase == pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE {
			naSeen = true
		}
	}
	if !naSeen {
		t.Fatal("older policy should be NOT_APPLICABLE (anti-rollback)")
	}
	if getActivePolicy().PolicyID != "new" {
		t.Fatalf("active policy downgraded to %q", getActivePolicy().PolicyID)
	}
}

func TestSetPolicy_Break_Malformed(t *testing.T) {
	resetPolicy(t)
	d, out, _ := policyDispatcher(t)
	for _, raw := range [][]byte{[]byte("not json"), []byte(`{"version":2,"policy_id":"p","issued_at":1}`), []byte(`{"version":1,"issued_at":1}`)} {
		d.handleSetPolicyCmd(context.Background(), nil, &pb.Command{CommandId: "bad", Type: "set_policy", Payload: raw})
		// A terminal status also emits a command_completed audit event, so scan
		// for the set_policy ActionStatus rather than assuming it is first.
		var failed bool
		for len(out) > 0 {
			if st := (<-out).GetActionStatus(); st != nil && st.ActionType == actionSetPolicy {
				if st.Phase != pb.ActionPhase_ACTION_PHASE_FAILED {
					t.Fatalf("malformed policy must FAIL, got %v", st.Phase)
				}
				failed = true
			}
		}
		if !failed {
			t.Fatal("expected a FAILED set_policy status")
		}
		if getActivePolicy() != nil {
			t.Fatal("malformed policy must not become active")
		}
	}
}

// loadStoredPolicy must RE-VERIFY the signature: a correctly-signed stored policy
// loads; a tampered one is ignored (not applied).
func TestLoadStoredPolicy_VerifiesSignature(t *testing.T) {
	resetPolicy(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	savedKey := cmdVerifyKey
	t.Cleanup(func() { cmdVerifyKey = savedKey })
	cmdVerifyKey = pub

	dir := t.TempDir()
	out := make(chan *pb.AgentMessage, 16)
	payload, _ := json.Marshal(policy.Policy{Version: 1, PolicyID: "signed", IssuedAt: 500})
	sig := commandsign.Sign(priv, "set_policy", "pol-x", payload)
	writeStore := func(sig []byte, payload []byte) {
		sp := storedPolicy{Type: "set_policy", CommandID: "pol-x", Payload: payload, Signature: sig}
		raw, _ := json.Marshal(sp)
		if err := os.WriteFile(filepath.Join(dir, "policy.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Valid signature → loaded.
	writeStore(sig, payload)
	loadStoredPolicy(dir, nil, out, "dev")
	if getActivePolicy() == nil || getActivePolicy().PolicyID != "signed" {
		t.Fatal("validly-signed policy should load")
	}

	// Tampered payload (signature no longer matches) → ignored + a tamper alert.
	policyMu.Lock()
	activePolicy = nil
	policyMu.Unlock()
	tampered, _ := json.Marshal(policy.Policy{Version: 1, PolicyID: "evil", IssuedAt: 999})
	writeStore(sig, tampered) // old sig, new payload
	loadStoredPolicy(dir, nil, out, "dev")
	if getActivePolicy() != nil {
		t.Fatal("tampered policy must be rejected by signature re-verification")
	}
	if countEvents(out, "policy_tampered") == 0 {
		t.Fatal("a modified policy must raise a policy_tampered alert")
	}
}

func TestLoadStoredPolicy_MissingIsNoop(t *testing.T) {
	resetPolicy(t)
	out := make(chan *pb.AgentMessage, 4)
	loadStoredPolicy(t.TempDir(), nil, out, "dev") // no file, no prior floor
	if getActivePolicy() != nil {
		t.Fatal("no stored policy should leave activePolicy nil")
	}
	if countEvents(out, "policy_tampered") != 0 {
		t.Fatal("a first-time missing policy (no floor) is NOT a tamper")
	}
}

// useInMemoryFloor swaps the sealed-floor storage for an in-memory blob so tests
// never touch the real Windows registry.
func useInMemoryFloor(t *testing.T) {
	t.Helper()
	savedR, savedW := readFloorBlob, writeFloorBlob
	var blob []byte
	readFloorBlob = func(string) ([]byte, error) { return blob, nil }
	writeFloorBlob = func(_ string, b []byte) error { blob = append([]byte(nil), b...); return nil }
	t.Cleanup(func() { readFloorBlob, writeFloorBlob = savedR, savedW })
}

// A2.5 — anti-rollback floor + tamper alerts, sealed with a real key provider.
func TestPolicyTamper_RollbackAndDeleteAndLockdown(t *testing.T) {
	resetPolicy(t)
	useInMemoryFloor(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	savedKey := cmdVerifyKey
	t.Cleanup(func() { cmdVerifyKey = savedKey })
	cmdVerifyKey = pub

	dir := t.TempDir()
	keys := secure.NewKeyProvider(dir)
	out := make(chan *pb.AgentMessage, 64)
	writeStore := func(policyID string, issuedAt int64) {
		payload, _ := json.Marshal(policy.Policy{Version: 1, PolicyID: policyID, IssuedAt: issuedAt})
		sig := commandsign.Sign(priv, "set_policy", "c", payload)
		sp := storedPolicy{Type: "set_policy", CommandID: "c", Payload: payload, Signature: sig}
		raw, _ := json.Marshal(sp)
		if err := os.WriteFile(filepath.Join(dir, "policy.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Apply a current policy (epoch 500) → raises the floor.
	writeStore("cur", 500)
	loadStoredPolicy(dir, keys, out, "dev")
	if getActivePolicy() == nil || getActivePolicy().PolicyID != "cur" {
		t.Fatal("current policy should apply and raise the floor")
	}
	drain(out)

	// ROLLBACK: a validly-signed but OLDER policy (epoch 100) must be refused.
	policyMu.Lock()
	activePolicy = nil
	policyMu.Unlock()
	writeStore("old", 100)
	loadStoredPolicy(dir, keys, out, "dev")
	if getActivePolicy() != nil {
		t.Fatal("rolled-back (older) policy must be refused by the sealed floor")
	}
	if countEvents(out, "policy_tampered") == 0 {
		t.Fatal("rollback must raise a tamper alert")
	}

	// DELETE: file gone but floor known → tamper. Repeat to cross the lockdown
	// threshold and raise the policy_lockdown "agent lock" alert.
	_ = os.Remove(filepath.Join(dir, "policy.json"))
	var lockdown int
	for i := 0; i < policyLockdownThreshold+1; i++ {
		loadStoredPolicy(dir, keys, out, "dev")
		lockdown += countEvents(out, "policy_lockdown")
	}
	if lockdown == 0 {
		t.Fatal("repeated tampering must raise a policy_lockdown alert")
	}
}

func drain(out chan *pb.AgentMessage) {
	for len(out) > 0 {
		<-out
	}
}

// The lockdown threshold is customer-tunable; a value < 1 keeps the default so a
// misconfig can never disable the lockdown alert.
func TestPolicyLockdownThresholdConfigurable(t *testing.T) {
	saved := policyLockdownThreshold
	t.Cleanup(func() { policyLockdownThreshold = saved })
	setPolicyLockdownThreshold(7)
	if policyLockdownThreshold != 7 {
		t.Fatalf("threshold should be 7, got %d", policyLockdownThreshold)
	}
	setPolicyLockdownThreshold(0) // invalid → keep previous
	if policyLockdownThreshold != 7 {
		t.Fatalf("invalid threshold must be ignored, got %d", policyLockdownThreshold)
	}
}

// A2 — the dispatch module gate blocks a command type the active policy denies.
func TestPolicyGate_DeniedCommandBlocked(t *testing.T) {
	resetPolicy(t)
	policyMu.Lock()
	activePolicy = &policy.Policy{Version: 1, PolicyID: "p", IssuedAt: 1, Modules: policy.Modules{Deny: []string{"run_command"}}}
	policyMu.Unlock()
	d, out, _ := policyDispatcher(t)
	d.dispatch(context.Background(), nil, &pb.Command{CommandId: "rc-1", Type: "run_command", Payload: []byte("{}")})
	var blocked bool
	for len(out) > 0 {
		if st := (<-out).GetActionStatus(); st != nil && st.Phase == pb.ActionPhase_ACTION_PHASE_FAILED && strings.Contains(st.Reason, "blocked by agent policy") {
			blocked = true
		}
	}
	if !blocked {
		t.Fatal("a policy-denied command type must be blocked by the dispatch gate")
	}
}

// A2.6 — fail-closed safe mode: after a tamper (policy expected but gone), the
// high-risk surface is disabled; set_policy + patch stay allowed; a valid policy
// clears safe mode.
func TestPolicySafeMode_FailClosedOnTamper(t *testing.T) {
	resetPolicy(t)
	useInMemoryFloor(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	savedKey := cmdVerifyKey
	t.Cleanup(func() { cmdVerifyKey = savedKey })
	cmdVerifyKey = pub

	dir := t.TempDir()
	keys := secure.NewKeyProvider(dir)
	out := make(chan *pb.AgentMessage, 32)
	writeStore := func(id string, issuedAt int64) {
		payload, _ := json.Marshal(policy.Policy{Version: 1, PolicyID: id, IssuedAt: issuedAt})
		sig := commandsign.Sign(priv, "set_policy", "c", payload)
		sp := storedPolicy{Type: "set_policy", CommandID: "c", Payload: payload, Signature: sig}
		raw, _ := json.Marshal(sp)
		_ = os.WriteFile(filepath.Join(dir, "policy.json"), raw, 0o600)
	}

	// Apply a policy (raises the floor, no safe mode).
	writeStore("cur", 500)
	loadStoredPolicy(dir, keys, out, "dev")
	if inSafeMode() {
		t.Fatal("a valid policy must not be in safe mode")
	}

	// Delete the policy → tamper → fail-closed safe mode.
	_ = os.Remove(filepath.Join(dir, "policy.json"))
	loadStoredPolicy(dir, keys, out, "dev")
	if !inSafeMode() {
		t.Fatal("a deleted-but-expected policy must engage safe mode")
	}
	if policyAllowsCommand("run_command") || policyAllowsCommand("open_shell") {
		t.Fatal("safe mode must DENY high-risk command types")
	}
	if !policyAllowsCommand("set_policy") || !policyAllowsCommand("install_patch") {
		t.Fatal("safe mode must still ALLOW set_policy (recovery) and patching")
	}
	if countEvents(out, "policy_safe_mode") == 0 {
		t.Fatal("entering safe mode must raise a policy_safe_mode audit event")
	}

	// A valid newer policy clears safe mode.
	writeStore("new", 600)
	loadStoredPolicy(dir, keys, out, "dev")
	if inSafeMode() {
		t.Fatal("a valid re-asserted policy must clear safe mode")
	}
	if !policyAllowsCommand("run_command") {
		t.Fatal("after recovery, run_command should be allowed again")
	}
}

// A3 — reboot_policy=never blocks an explicit reboot command.
func TestPolicyReboot_NeverBlocks(t *testing.T) {
	resetPolicy(t)
	policyMu.Lock()
	activePolicy = &policy.Policy{Version: 1, PolicyID: "p", IssuedAt: 1, Patch: policy.Patch{RebootPolicy: "never"}}
	policyMu.Unlock()
	d, out, _ := policyDispatcher(t)
	d.handleRebootCmd(context.Background(), nil, &pb.Command{CommandId: "rb-1", Type: "reboot", Payload: []byte{}})
	var blocked bool
	for len(out) > 0 {
		if st := (<-out).GetActionStatus(); st != nil && st.Phase == pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE && strings.Contains(st.Reason, "reboot_policy=never") {
			blocked = true
		}
	}
	if !blocked {
		t.Fatal("reboot_policy=never must block an explicit reboot command")
	}
}

// set_policy is always allowed (no lock-out); a strict allow-list blocks the rest.
func TestPolicyGate_SetPolicyAlwaysAllowed(t *testing.T) {
	resetPolicy(t)
	policyMu.Lock()
	activePolicy = &policy.Policy{Version: 1, PolicyID: "p", IssuedAt: 1, Modules: policy.Modules{Allow: []string{"scan_now"}, Deny: []string{"set_policy"}}}
	policyMu.Unlock()
	if !policyAllowsCommand("set_policy") {
		t.Fatal("set_policy must ALWAYS be allowed — cannot lock out policy updates")
	}
	if !policyAllowsCommand("scan_now") {
		t.Fatal("allow-listed command must pass")
	}
	if policyAllowsCommand("run_command") {
		t.Fatal("non-allow-listed command must be blocked")
	}
}
