package policy

import (
	"encoding/json"
	"testing"
)

func mustJSON(t *testing.T, p Policy) []byte {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseValidate(t *testing.T) {
	good := Policy{Version: 1, PolicyID: "p1", IssuedAt: 1000, Patch: Patch{RebootPolicy: "if_required"}, Logging: Logging{Level: "warn"}, Maintenance: Maintenance{StartHour: 1, EndHour: 5}}
	if _, err := Parse(mustJSON(t, good)); err != nil {
		t.Fatalf("good policy rejected: %v", err)
	}
	bad := []Policy{
		{Version: 2, PolicyID: "p", IssuedAt: 1},                              // version
		{Version: 1, PolicyID: "", IssuedAt: 1},                              // no id
		{Version: 1, PolicyID: "p", IssuedAt: 0},                             // no issued_at
		{Version: 1, PolicyID: "p", IssuedAt: 1, Patch: Patch{RebootPolicy: "bogus"}},
		{Version: 1, PolicyID: "p", IssuedAt: 1, Transfer: Transfer{FetchMaxBPS: -5}},
		{Version: 1, PolicyID: "p", IssuedAt: 1, Logging: Logging{Level: "verbose"}},
		{Version: 1, PolicyID: "p", IssuedAt: 1, Maintenance: Maintenance{StartHour: 25}},
	}
	for i, p := range bad {
		if _, err := Parse(mustJSON(t, p)); err == nil {
			t.Fatalf("bad policy %d accepted", i)
		}
	}
	if _, err := Parse([]byte("not json")); err == nil {
		t.Fatal("malformed JSON accepted")
	}
}

func TestSupersedes(t *testing.T) {
	p100 := &Policy{IssuedAt: 100}
	p200 := &Policy{IssuedAt: 200}
	if !p100.Supersedes(nil) {
		t.Fatal("any policy supersedes nil")
	}
	if !p200.Supersedes(p100) {
		t.Fatal("newer must supersede older")
	}
	if p100.Supersedes(p200) {
		t.Fatal("older must NOT supersede newer (anti-rollback)")
	}
	if p100.Supersedes(p100) {
		t.Fatal("equal issued_at must not supersede (no downgrade/replay)")
	}
}

func TestCommandAllowed(t *testing.T) {
	// nil policy = allow all.
	if !(*Policy)(nil).CommandAllowed("run_command") {
		t.Fatal("nil policy must allow everything")
	}
	// Deny wins.
	deny := &Policy{Modules: Modules{Deny: []string{"run_command", "open_shell"}}}
	if deny.CommandAllowed("Run_Command") { // case-insensitive
		t.Fatal("denied type must be blocked")
	}
	if !deny.CommandAllowed("install_patch") {
		t.Fatal("non-denied type must pass when no allow-list")
	}
	// Allow-list is strict.
	allow := &Policy{Modules: Modules{Allow: []string{"install_patch", "scan_now"}}}
	if !allow.CommandAllowed("install_patch") {
		t.Fatal("allow-listed type must pass")
	}
	if allow.CommandAllowed("run_command") {
		t.Fatal("non-allow-listed type must be blocked under a strict allow-list")
	}
	// Deny beats allow.
	both := &Policy{Modules: Modules{Allow: []string{"run_command"}, Deny: []string{"run_command"}}}
	if both.CommandAllowed("run_command") {
		t.Fatal("deny must win over allow")
	}
}
