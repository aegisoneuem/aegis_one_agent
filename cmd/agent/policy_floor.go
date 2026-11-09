package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
)

// policyLockdownThreshold is how many tamper events (modify/rollback/delete)
// trigger the high-severity policy_lockdown alert — the "agent lock" signal the
// console raises on repeated abuse (a downgrade brute-force or policy-DoS).
const policyLockdownThreshold = 3

// policyFloor is the persisted anti-rollback + tamper state. It is sealed at rest
// with the DPAPI-protected master key (machine-bound), so a casual local admin
// cannot read or reset it; a TPM NV monotonic counter is the roadmap upgrade for
// a truly tamper-proof floor an administrator cannot roll back either.
type policyFloor struct {
	MinIssuedAt int64 `json:"min_issued_at"` // highest policy issued_at ever applied (anti-rollback)
	TamperCount int   `json:"tamper_count"`  // consecutive tamper events since the last good policy
	LastTamper  int64 `json:"last_tamper_ts,omitempty"`
}

func policyFloorPath(secureDir string) string {
	return filepath.Join(secureDir, "policy.floor")
}

// loadPolicyFloor returns the sealed floor, or a zero floor when absent/unreadable.
func loadPolicyFloor(keys secure.KeyProvider, secureDir string) policyFloor {
	data, err := os.ReadFile(policyFloorPath(secureDir))
	if err != nil || keys == nil {
		return policyFloor{}
	}
	key, err := keys.Key()
	if err != nil {
		return policyFloor{}
	}
	plain, err := secure.Open(key, data)
	if err != nil {
		return policyFloor{} // corrupt/tampered seal → treated as reset (a deletion is itself detected via the policy file)
	}
	var f policyFloor
	if err := json.Unmarshal(plain, &f); err != nil {
		return policyFloor{}
	}
	return f
}

// savePolicyFloor seals and atomically writes the floor (0600).
func savePolicyFloor(keys secure.KeyProvider, secureDir string, f policyFloor) error {
	if keys == nil {
		return nil // best-effort; no key provider in some test paths
	}
	key, err := keys.Key()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	blob, err := secure.Seal(key, raw)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(secureDir, 0o700); err != nil {
		return err
	}
	path := policyFloorPath(secureDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
