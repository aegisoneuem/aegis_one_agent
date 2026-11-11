package main

import (
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	"github.com/aegisoneuem/aegis_one_agent/internal/logging"
	"github.com/aegisoneuem/aegis_one_agent/internal/policy"
	"github.com/aegisoneuem/aegis_one_agent/internal/throttle"
)

func setActivePolicyForTest(t *testing.T, p *policy.Policy) {
	t.Helper()
	policyMu.Lock()
	prev := activePolicy
	activePolicy = p
	policyMu.Unlock()
	t.Cleanup(func() {
		policyMu.Lock()
		activePolicy = prev
		policyMu.Unlock()
	})
}

// A signed policy's logging.level is applied live by applyPolicy (A3).
func TestApplyPolicy_LoggingLevelOverride(t *testing.T) {
	logging.SetLevel("info")
	t.Cleanup(func() { logging.SetLevel("info") })

	applyPolicy(&policy.Policy{Version: 1, PolicyID: "p", IssuedAt: 1, Logging: policy.Logging{Level: "debug"}})
	if logging.CurrentLevel() != slog.LevelDebug {
		t.Fatalf("policy logging.level=debug not applied: %v", logging.CurrentLevel())
	}
}

// transfer.fetch_max_bytes_per_second is enforced live, lower-wins against the
// agent's own cap.
func TestPolicyFetchMaxBPS_LowerWins(t *testing.T) {
	setActivePolicyForTest(t, nil)
	if policyFetchMaxBPS() != 0 {
		t.Fatalf("no policy must yield 0 cap")
	}

	setActivePolicyForTest(t, &policy.Policy{Version: 1, PolicyID: "p", IssuedAt: 1, Transfer: policy.Transfer{FetchMaxBPS: 1000}})
	if policyFetchMaxBPS() != 1000 {
		t.Fatalf("policy fetch cap not returned: %d", policyFetchMaxBPS())
	}
	if got := throttle.EffectiveRate(2000, policyFetchMaxBPS()); got != 1000 {
		t.Fatalf("policy cap must win over a higher agent cap: %d", got)
	}
	if got := throttle.EffectiveRate(500, policyFetchMaxBPS()); got != 500 {
		t.Fatalf("a lower agent cap must win: %d", got)
	}
}

// applyPolicy retunes the audit flood-control guard; a burst of 1 means only the
// first of several distinct command_received events is admitted.
func TestApplyPolicy_AuditOverrideTakesEffect(t *testing.T) {
	prev := auditGuard
	t.Cleanup(func() { auditGuard = prev })
	auditGuard = auditlog.NewGuard(50, 100, 2*time.Second)

	applyPolicy(&policy.Policy{Version: 1, PolicyID: "p", IssuedAt: 1, Audit: policy.Audit{RatePerSec: 1, Burst: 1, DedupWindowSeconds: 1}})

	admitted := 0
	for i := 0; i < 5; i++ {
		if w, _ := auditGuard.Admit(auditlog.Event{DeviceID: "d", EventType: "command_received", CommandID: fmt.Sprintf("c%d", i)}); w {
			admitted++
		}
	}
	if admitted != 1 {
		t.Fatalf("policy audit override (burst=1) not in effect: admitted=%d (want 1)", admitted)
	}
}
