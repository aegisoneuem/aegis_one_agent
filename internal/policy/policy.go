// Package policy is the agent's signed policy document — the masthead-equivalent
// the server pushes to declare the security floor, which command modules are
// enabled, and behavior settings. The document is carried as the JSON payload of
// an Ed25519-signed set_policy command; the command envelope's signature IS the
// policy's signature (no separate signing). This package is pure data + rules so
// it is trivially unit-testable; storage, verification and enforcement live in
// the agent (cmd/agent/policy.go).
package policy

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Policy is the agent policy document (version 1).
type Policy struct {
	Version       int           `json:"version"`
	PolicyID      string        `json:"policy_id"`
	IssuedAt      int64         `json:"issued_at"` // unix seconds; used for anti-rollback
	SecurityFloor SecurityFloor `json:"security_floor"`
	Modules       Modules       `json:"modules"`  // consumed in step A2 (command gate)
	Behavior      Behavior      `json:"behavior"` // consumed in step A3 (behavior config)
}

// SecurityFloor is the minimum security posture the endpoint must meet.
type SecurityFloor struct {
	RequireFIPS bool   `json:"require_fips,omitempty"`
	MinTLS      string `json:"min_tls,omitempty"` // informational; agent already enforces TLS 1.3
}

// Modules controls which signed command types the agent will execute. A non-empty
// Allow is a strict allow-list (only those run). Deny always wins over Allow.
// Empty/empty = everything allowed (default).
type Modules struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// Behavior holds policy-driven behavior settings (applied in A3).
type Behavior struct {
	RebootPolicy string `json:"reboot_policy,omitempty"` // "" | never | if_required | always
	FetchMaxBPS  int64  `json:"fetch_max_bytes_per_second,omitempty"`
}

// Parse decodes and validates a policy document.
func Parse(raw []byte) (*Policy, error) {
	var p Policy
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid policy JSON: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate rejects a malformed policy fail-loud.
func (p *Policy) Validate() error {
	if p.Version != 1 {
		return fmt.Errorf("unsupported policy version %d (want 1)", p.Version)
	}
	if strings.TrimSpace(p.PolicyID) == "" {
		return fmt.Errorf("policy_id is required")
	}
	if p.IssuedAt <= 0 {
		return fmt.Errorf("issued_at must be a positive unix timestamp")
	}
	switch strings.ToLower(strings.TrimSpace(p.Behavior.RebootPolicy)) {
	case "", "never", "if_required", "always":
	default:
		return fmt.Errorf("invalid reboot_policy %q (want never|if_required|always)", p.Behavior.RebootPolicy)
	}
	if p.Behavior.FetchMaxBPS < 0 {
		return fmt.Errorf("fetch_max_bytes_per_second must not be negative")
	}
	return nil
}

// Supersedes reports whether p is newer than prev (anti-rollback). A nil prev is
// always superseded. Ties on IssuedAt go to a different PolicyID being accepted
// only when strictly newer, so replaying an old signed policy cannot downgrade.
func (p *Policy) Supersedes(prev *Policy) bool {
	if prev == nil {
		return true
	}
	return p.IssuedAt > prev.IssuedAt
}

// CommandAllowed reports whether a signed command type may run under this policy.
// Deny wins; a non-empty Allow is a strict allow-list. Matching is case-insensitive.
// A nil policy allows everything (no policy = no restriction).
func (p *Policy) CommandAllowed(cmdType string) bool {
	if p == nil {
		return true
	}
	ct := strings.ToLower(strings.TrimSpace(cmdType))
	for _, d := range p.Modules.Deny {
		if strings.ToLower(strings.TrimSpace(d)) == ct {
			return false
		}
	}
	if len(p.Modules.Allow) == 0 {
		return true
	}
	for _, a := range p.Modules.Allow {
		if strings.ToLower(strings.TrimSpace(a)) == ct {
			return true
		}
	}
	return false
}

// Summary is a short human/audit string describing the policy.
func (p *Policy) Summary() string {
	if p == nil {
		return "none"
	}
	return fmt.Sprintf("policy_id=%s issued_at=%d require_fips=%t deny=%d allow=%d reboot_policy=%q",
		p.PolicyID, p.IssuedAt, p.SecurityFloor.RequireFIPS, len(p.Modules.Deny), len(p.Modules.Allow), p.Behavior.RebootPolicy)
}
