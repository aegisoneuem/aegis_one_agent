package secure

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	key := testKey(t)
	plaintext := []byte("missing: KB5129195, KB5126052 — device posture data")

	blob, err := Seal(key, plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(blob, plaintext) {
		t.Fatal("ciphertext still contains the plaintext")
	}

	got, err := Open(key, blob)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plaintext)
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	key := testKey(t)
	blob, err := Seal(key, []byte("sensitive"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Flip a bit in the last byte (inside the tag/ciphertext).
	blob[len(blob)-1] ^= 0x01
	if _, err := Open(key, blob); err == nil {
		t.Fatal("Open accepted tampered ciphertext — GCM auth must fail loud")
	}
}

func TestOpenRejectsWrongKey(t *testing.T) {
	blob, err := Seal(testKey(t), []byte("sensitive"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := Open(testKey(t), blob); err == nil {
		t.Fatal("Open accepted a different key — must fail")
	}
}

func TestSealRejectsBadKeySize(t *testing.T) {
	if _, err := Seal(make([]byte, 16), []byte("x")); err == nil {
		t.Fatal("Seal accepted a 16-byte key — must require 32")
	}
}
