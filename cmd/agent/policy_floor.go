package main

import (
	"encoding/json"

	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
)

// policyLockdownThreshold is how many tamper events (modify/rollback/delete)
// trigger the high-severity policy_lockdown alert — the "agent lock" signal the
// console raises on repeated abuse (a downgrade brute-force or policy-DoS).
// Customer-tunable via AEGIS_POLICY_LOCKDOWN_THRESHOLD; set once at startup.
var policyLockdownThreshold = 3

// setPolicyLockdownThreshold applies the configured threshold (values < 1 keep
// the default so a misconfig cannot disable the lockdown alert).
func setPolicyLockdownThreshold(n int) {
	if n >= 1 {
		policyLockdownThreshold = n
	}
}

// policyFloor is the persisted anti-rollback + tamper state. It is SEALED with
// the DPAPI-protected master key (machine-bound) and, on Windows, stored in the
// HKLM\SOFTWARE\AegisOne registry tree (NOT a file next to policy.json), so a
// local admin cannot easily find + delete it alongside the policy and cannot read
// or forge it. A TPM NV monotonic counter is the roadmap upgrade for a floor an
// administrator cannot reset at all. readFloorBlob/writeFloorBlob are the
// platform-specific storage (registry on Windows, a file elsewhere).
type policyFloor struct {
	MinIssuedAt int64 `json:"min_issued_at"` // highest policy issued_at ever applied (anti-rollback)
	TamperCount int   `json:"tamper_count"`  // consecutive tamper events since the last good policy
	LastTamper  int64 `json:"last_tamper_ts,omitempty"`
}

// readFloorBlob/writeFloorBlob are the platform storage for the sealed floor
// (registry on Windows, a file elsewhere). They are function vars so tests can
// substitute an in-memory store and never touch the real registry.
var (
	readFloorBlob  = platformReadFloorBlob
	writeFloorBlob = platformWriteFloorBlob
)

// loadPolicyFloor returns the sealed floor, or a zero floor when absent/unreadable.
func loadPolicyFloor(keys secure.KeyProvider, secureDir string) policyFloor {
	data, err := readFloorBlob(secureDir)
	if err != nil || len(data) == 0 || keys == nil {
		return policyFloor{}
	}
	key, err := keys.Key()
	if err != nil {
		return policyFloor{}
	}
	plain, err := secure.Open(key, data)
	if err != nil {
		return policyFloor{} // corrupt/tampered seal → treated as reset (deletion is detected via the policy file)
	}
	var f policyFloor
	if err := json.Unmarshal(plain, &f); err != nil {
		return policyFloor{}
	}
	return f
}

// savePolicyFloor seals and persists the floor to the platform store.
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
	return writeFloorBlob(secureDir, blob)
}
