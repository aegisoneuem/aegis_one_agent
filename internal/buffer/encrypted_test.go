package buffer

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// staticKey is a test KeyProvider with a fixed 32-byte key (no DPAPI needed, so
// this test runs on every platform).
type staticKey struct{ k []byte }

func (s staticKey) Key() ([]byte, error) { return s.k, nil }

func newTestKey() staticKey {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return staticKey{k: k}
}

func TestEncryptedBufferRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "buf.jsonl")
	b, err := NewEncryptedFileBuffer(path, newTestKey())
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	payload := []byte("missing: KB5129195 (Critical) build 26200.9168")
	if err := b.Enqueue("patch_scan_result", payload); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// On-disk bytes must NOT contain the plaintext payload.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if bytes.Contains(raw, []byte("KB5129195")) {
		t.Fatal("plaintext payload found on disk — buffer is not encrypting")
	}

	var got [][]byte
	n, err := b.Drain(func(kind string, p []byte) error {
		if kind != "patch_scan_result" {
			t.Errorf("unexpected kind %q", kind)
		}
		got = append(got, append([]byte(nil), p...))
		return nil
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if n != 1 || len(got) != 1 || !bytes.Equal(got[0], payload) {
		t.Fatalf("round trip failed: n=%d got=%v", n, got)
	}
}

func TestEncryptedBufferDropsTampered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "buf.jsonl")
	b, err := NewEncryptedFileBuffer(path, newTestKey())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := b.Enqueue("k", []byte("secret")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Corrupt the ciphertext while keeping the JSON valid: flip the first
	// character of the base64 payload value (which decodes into the GCM nonce),
	// so the record still parses but fails authentication on decode.
	raw, _ := os.ReadFile(path)
	marker := []byte(`"payload":"`)
	idx := bytes.Index(raw, marker)
	if idx < 0 {
		t.Fatal("could not locate payload field")
	}
	pos := idx + len(marker)
	if raw[pos] == 'A' {
		raw[pos] = 'B'
	} else {
		raw[pos] = 'A'
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	handled := 0
	n, err := b.Drain(func(kind string, p []byte) error { handled++; return nil })
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if handled != 0 {
		t.Fatal("tampered record was handed to the caller — must be dropped")
	}
	if n != 1 {
		t.Fatalf("tampered record should be counted as removed, got n=%d", n)
	}
}

// A different key must not be able to read another key's records.
func TestEncryptedBufferWrongKeyCannotRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "buf.jsonl")
	b, err := NewEncryptedFileBuffer(path, newTestKey())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := b.Enqueue("k", []byte("secret")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	other := staticKey{k: make([]byte, 32)} // all-zero key
	b2, err := NewEncryptedFileBuffer(path, other)
	if err != nil {
		t.Fatalf("new2: %v", err)
	}
	handled := 0
	if _, err := b2.Drain(func(kind string, p []byte) error { handled++; return nil }); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if handled != 0 {
		t.Fatal("wrong key could read the record — must not decrypt")
	}
}
