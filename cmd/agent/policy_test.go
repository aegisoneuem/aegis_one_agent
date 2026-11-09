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
	"github.com/aegisoneuem/aegis_one_proto/commandsign"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

func resetPolicy(t *testing.T) {
	t.Helper()
	saved := activePolicy
	t.Cleanup(func() {
		policyMu.Lock()
		activePolicy = saved
		policyMu.Unlock()
	})
	policyMu.Lock()
	activePolicy = nil
	policyMu.Unlock()
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

	secure := t.TempDir()
	payload, _ := json.Marshal(policy.Policy{Version: 1, PolicyID: "signed", IssuedAt: 500})
	sig := commandsign.Sign(priv, "set_policy", "pol-x", payload)
	writeStore := func(sig []byte, payload []byte) {
		sp := storedPolicy{Type: "set_policy", CommandID: "pol-x", Payload: payload, Signature: sig}
		raw, _ := json.Marshal(sp)
		if err := os.WriteFile(filepath.Join(secure, "policy.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Valid signature → loaded.
	writeStore(sig, payload)
	loadStoredPolicy(secure)
	if getActivePolicy() == nil || getActivePolicy().PolicyID != "signed" {
		t.Fatal("validly-signed policy should load")
	}

	// Tampered payload (signature no longer matches) → ignored.
	policyMu.Lock()
	activePolicy = nil
	policyMu.Unlock()
	tampered, _ := json.Marshal(policy.Policy{Version: 1, PolicyID: "evil", IssuedAt: 999})
	writeStore(sig, tampered) // old sig, new payload
	loadStoredPolicy(secure)
	if getActivePolicy() != nil {
		t.Fatal("tampered policy must be rejected by signature re-verification")
	}
}

func TestLoadStoredPolicy_MissingIsNoop(t *testing.T) {
	resetPolicy(t)
	loadStoredPolicy(t.TempDir()) // no file
	if getActivePolicy() != nil {
		t.Fatal("no stored policy should leave activePolicy nil")
	}
}

func drain(out chan *pb.AgentMessage) {
	for len(out) > 0 {
		<-out
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
