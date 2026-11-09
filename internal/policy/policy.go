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

// Policy is the agent policy document (version 1) — the full, Ivanti-equivalent
// set of agent settings the server can turn on/off or tune, carried in ONE signed
// JSON document so a customer controls everything without a new agent binary. An
// empty section is "do not override the agent default".
type Policy struct {
	Version       int           `json:"version"`
	PolicyID      string        `json:"policy_id"`
	IssuedAt      int64         `json:"issued_at"` // unix seconds; used for anti-rollback
	SecurityFloor SecurityFloor `json:"security_floor"`
	Modules       Modules       `json:"modules"`     // capability on/off (A2 — enforced)
	Patch         Patch         `json:"patch"`       // patch + reboot behavior
	Inventory     Inventory     `json:"inventory"`   // scan schedule/triggers
	Transfer      Transfer      `json:"transfer"`    // bandwidth caps, peer cache
	Maintenance   Maintenance   `json:"maintenance"` // window when installs/reboots run
	Audit         Audit         `json:"audit"`       // audit flood-control tuning
	Logging       Logging       `json:"logging"`     // log verbosity
	CheckIn       CheckIn       `json:"checkin"`     // heartbeat interval
}

// SecurityFloor is the minimum security posture the endpoint must meet.
type SecurityFloor struct {
	RequireFIPS           bool   `json:"require_fips,omitempty"`
	RequireSignedCommands bool   `json:"require_signed_commands,omitempty"` // always true in practice; declared for the contract
	MinTLS                string `json:"min_tls,omitempty"`                 // informational; agent already enforces TLS 1.3
}

// Modules controls which signed command types the agent will execute. A non-empty
// Allow is a strict allow-list (only those run). Deny always wins over Allow.
// Empty/empty = everything allowed (default). This is the per-capability on/off.
type Modules struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// Patch holds patch + reboot behavior.
type Patch struct {
	RebootPolicy          string `json:"reboot_policy,omitempty"` // "" | never | if_required | always
	InstallIfRebootPending *bool `json:"install_if_reboot_pending,omitempty"`
	DirectVendorDownload   *bool `json:"direct_vendor_download,omitempty"`
	ScanIntervalSeconds   int64  `json:"scan_interval_seconds,omitempty"`
	MaxAttempts           int    `json:"max_attempts,omitempty"`
}

// Inventory holds inventory scan schedule + triggers.
type Inventory struct {
	FullIntervalSeconds        int64 `json:"full_interval_seconds,omitempty"`
	DeviceStateIntervalSeconds int64 `json:"device_state_interval_seconds,omitempty"`
	ScanOnLogin                *bool `json:"scan_on_login,omitempty"`
	ScanOnIPChange             *bool `json:"scan_on_ip_change,omitempty"`
}

// Transfer holds bandwidth caps + peer cache.
type Transfer struct {
	FetchMaxBPS      int64 `json:"fetch_max_bytes_per_second,omitempty"`
	UploadMaxBPS     int64 `json:"upload_max_bytes_per_second,omitempty"`
	PeerCacheEnabled *bool `json:"peer_cache_enabled,omitempty"`
}

// Maintenance restricts installs/reboots to an allowed weekly window.
type Maintenance struct {
	Days      []string `json:"days,omitempty"`       // mon,tue,wed,thu,fri,sat,sun (empty = any day)
	StartHour int      `json:"start_hour,omitempty"` // 0-23 local
	EndHour   int      `json:"end_hour,omitempty"`   // 0-23 local (exclusive); 0==0 means unset
}

// Audit tunes the flood-control guard.
type Audit struct {
	RatePerSec         int   `json:"rate_per_sec,omitempty"`
	Burst              int   `json:"burst,omitempty"`
	DedupWindowSeconds int64 `json:"dedup_window_seconds,omitempty"`
}

// Logging sets the log verbosity.
type Logging struct {
	Level string `json:"level,omitempty"` // "" | debug | info | warn | error
}

// CheckIn sets the heartbeat interval.
type CheckIn struct {
	HeartbeatIntervalSeconds int64 `json:"heartbeat_interval_seconds,omitempty"`
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
	switch strings.ToLower(strings.TrimSpace(p.Patch.RebootPolicy)) {
	case "", "never", "if_required", "always":
	default:
		return fmt.Errorf("invalid reboot_policy %q (want never|if_required|always)", p.Patch.RebootPolicy)
	}
	switch strings.ToLower(strings.TrimSpace(p.Logging.Level)) {
	case "", "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid logging level %q (want debug|info|warn|error)", p.Logging.Level)
	}
	if p.Transfer.FetchMaxBPS < 0 || p.Transfer.UploadMaxBPS < 0 {
		return fmt.Errorf("transfer bandwidth caps must not be negative")
	}
	if p.Maintenance.StartHour < 0 || p.Maintenance.StartHour > 23 || p.Maintenance.EndHour < 0 || p.Maintenance.EndHour > 23 {
		return fmt.Errorf("maintenance window hours must be 0-23")
	}
	for _, neg := range []int64{p.Patch.ScanIntervalSeconds, p.Inventory.FullIntervalSeconds, p.Inventory.DeviceStateIntervalSeconds, p.Audit.DedupWindowSeconds, p.CheckIn.HeartbeatIntervalSeconds} {
		if neg < 0 {
			return fmt.Errorf("interval/duration values must not be negative")
		}
	}
	return nil
}

// RebootPolicy returns the effective reboot policy ("" when unset → agent default).
func (p *Policy) RebootPolicy() string {
	if p == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(p.Patch.RebootPolicy))
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
		p.PolicyID, p.IssuedAt, p.SecurityFloor.RequireFIPS, len(p.Modules.Deny), len(p.Modules.Allow), p.Patch.RebootPolicy)
}
