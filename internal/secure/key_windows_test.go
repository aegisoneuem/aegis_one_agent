//go:build windows

package secure

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestDPAPIKeyProviderRoundTrip exercises the real DPAPI binding: create a key,
// persist the protected blob, then load it back through a fresh provider.
func TestDPAPIKeyProviderRoundTrip(t *testing.T) {
	dir := t.TempDir()

	p1 := NewKeyProvider(dir)
	k1, err := p1.Key()
	if err != nil {
		t.Fatalf("first Key(): %v", err)
	}
	if len(k1) != KeySize {
		t.Fatalf("key size %d, want %d", len(k1), KeySize)
	}

	// The on-disk blob must NOT contain the raw key (it is DPAPI-protected).
	blob, err := os.ReadFile(filepath.Join(dir, keyFileName))
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if bytes.Contains(blob, k1) {
		t.Fatal("on-disk key blob contains the raw key — DPAPI protection failed")
	}

	// A fresh provider over the same dir must unprotect the same key.
	p2 := NewKeyProvider(dir)
	k2, err := p2.Key()
	if err != nil {
		t.Fatalf("reload Key(): %v", err)
	}
	if !bytes.Equal(k1, k2) {
		t.Fatal("reloaded key does not match the original")
	}

	// Data sealed with one load opens with the reloaded key.
	msg := []byte("device posture + missing KBs")
	sealed, err := Seal(k1, msg)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := Open(k2, sealed)
	if err != nil {
		t.Fatalf("Open with reloaded key: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatal("cross-load round trip mismatch")
	}
}

func TestDPAPIProtectUnprotect(t *testing.T) {
	data := []byte("hello dpapi")
	ent := []byte("test-entropy")
	blob, err := dpapiProtect(data, ent)
	if err != nil {
		t.Fatalf("protect: %v", err)
	}
	if bytes.Contains(blob, data) {
		t.Fatal("protected blob contains plaintext")
	}
	got, err := dpapiUnprotect(blob, ent)
	if err != nil {
		t.Fatalf("unprotect: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("round trip mismatch: %q", got)
	}
	// Wrong entropy must fail.
	if _, err := dpapiUnprotect(blob, []byte("wrong")); err == nil {
		t.Fatal("unprotect succeeded with wrong entropy — must fail")
	}
}
