package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// keyTestHarness swaps the marker store for an in-memory flag and captures alert
// kinds, so the integrity check is tested without the registry or the outbox.
func keyTestHarness(t *testing.T) (dir string, marker *bool, kinds *[]string) {
	t.Helper()
	dir = t.TempDir()
	m := false
	var got []string

	prevRead, prevWrite, prevAlert := readKeyMarker, writeKeyMarker, keyAlert
	readKeyMarker = func(string) bool { return m }
	writeKeyMarker = func(string) error { m = true; return nil }
	keyAlert = func(_ chan<- *pb.AgentMessage, _, kind, _ string) { got = append(got, kind) }
	t.Cleanup(func() { readKeyMarker, writeKeyMarker, keyAlert = prevRead, prevWrite, prevAlert })

	return dir, &m, &got
}

func keyPath(dir string) string { return filepath.Join(dir, secure.KeyFileName) }

// A genuine first install (no key, no marker) is silent and leaves a good key +
// a marker behind.
func TestKeyIntegrity_FirstInstall_NoAlert(t *testing.T) {
	dir, marker, kinds := keyTestHarness(t)

	checkKeyIntegrity(dir, secure.NewKeyProvider(dir), nil, "dev")

	if len(*kinds) != 0 {
		t.Fatalf("first install must not alert, got %v", *kinds)
	}
	if !*marker {
		t.Fatalf("marker must be written after provisioning")
	}
	if _, err := os.Stat(keyPath(dir)); err != nil {
		t.Fatalf("key file must exist after first install: %v", err)
	}
}

// A key file that unseals cleanly is healthy — no alert, marker ensured.
func TestKeyIntegrity_HealthyKey_NoAlert(t *testing.T) {
	dir, _, kinds := keyTestHarness(t)
	if _, err := secure.NewKeyProvider(dir).Key(); err != nil { // provision a real sealed key
		t.Fatalf("provision: %v", err)
	}

	checkKeyIntegrity(dir, secure.NewKeyProvider(dir), nil, "dev")
	if len(*kinds) != 0 {
		t.Fatalf("healthy key must not alert, got %v", *kinds)
	}
}

// A deleted key (marker present, file gone) raises a 'deleted' alert and the key
// is regenerated.
func TestKeyIntegrity_Deleted_Alerts(t *testing.T) {
	dir, marker, kinds := keyTestHarness(t)
	*marker = true // we had a key before

	checkKeyIntegrity(dir, secure.NewKeyProvider(dir), nil, "dev")

	if len(*kinds) != 1 || (*kinds)[0] != "deleted" {
		t.Fatalf("expected one 'deleted' alert, got %v", *kinds)
	}
	if _, err := os.Stat(keyPath(dir)); err != nil {
		t.Fatalf("key must be regenerated (self-heal): %v", err)
	}
}

// A corrupt key blob raises a 'corrupt' alert, the bad blob is dropped, and a
// fresh key is generated.
func TestKeyIntegrity_Corrupt_AlertsAndSelfHeals(t *testing.T) {
	dir, marker, kinds := keyTestHarness(t)
	*marker = true
	if err := os.WriteFile(keyPath(dir), []byte("not-a-valid-dpapi-blob"), 0o600); err != nil {
		t.Fatalf("seed corrupt key: %v", err)
	}

	checkKeyIntegrity(dir, secure.NewKeyProvider(dir), nil, "dev")

	if len(*kinds) != 1 || (*kinds)[0] != "corrupt" {
		t.Fatalf("expected one 'corrupt' alert, got %v", *kinds)
	}
	// self-heal: a fresh, unsealable key is now present.
	if _, err := secure.NewKeyProvider(dir).Key(); err != nil {
		t.Fatalf("key not self-healed: %v", err)
	}
}
