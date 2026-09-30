package secure

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	return p
}

func TestEncryptDecryptFileRoundTrip(t *testing.T) {
	key := testKey(t)

	sizes := []int{
		0,                       // empty
		1,                       // tiny
		defaultChunkSize - 1,    // just under one chunk
		defaultChunkSize,        // exactly one chunk
		defaultChunkSize + 1,    // just over one chunk
		defaultChunkSize*3 + 123, // several chunks + remainder
	}
	for _, n := range sizes {
		data := make([]byte, n)
		if _, err := rand.Read(data); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		src := filepath.Join(dir, "plain")
		enc := filepath.Join(dir, "enc")
		dec := filepath.Join(dir, "dec")
		if err := os.WriteFile(src, data, 0o600); err != nil {
			t.Fatal(err)
		}

		if err := EncryptFile(key, src, enc); err != nil {
			t.Fatalf("size %d: encrypt: %v", n, err)
		}
		// Ciphertext must not contain a big run of the plaintext (spot check for
		// non-empty inputs).
		if n > 32 {
			encBytes, _ := os.ReadFile(enc)
			if bytes.Contains(encBytes, data[:32]) {
				t.Fatalf("size %d: plaintext prefix found in ciphertext", n)
			}
		}
		if err := DecryptFile(key, enc, dec); err != nil {
			t.Fatalf("size %d: decrypt: %v", n, err)
		}
		got, _ := os.ReadFile(dec)
		if !bytes.Equal(got, data) {
			t.Fatalf("size %d: round-trip mismatch (got %d bytes)", n, len(got))
		}
	}
}

func TestDecryptFileWrongKey(t *testing.T) {
	dir := t.TempDir()
	src := writeTemp(t, "p", bytes.Repeat([]byte("x"), 5000))
	enc := filepath.Join(dir, "enc")
	dec := filepath.Join(dir, "dec")
	if err := EncryptFile(testKey(t), src, enc); err != nil {
		t.Fatal(err)
	}
	if err := DecryptFile(testKey(t), enc, dec); err == nil {
		t.Fatal("decrypt with wrong key succeeded — must fail")
	}
	if _, err := os.Stat(dec); !os.IsNotExist(err) {
		t.Fatal("partial plaintext left on disk after failed decrypt")
	}
}

func TestDecryptFileDetectsTamper(t *testing.T) {
	dir := t.TempDir()
	key := testKey(t)
	src := writeTemp(t, "p", bytes.Repeat([]byte("secret-data-"), 500))
	enc := filepath.Join(dir, "enc")
	dec := filepath.Join(dir, "dec")
	if err := EncryptFile(key, src, enc); err != nil {
		t.Fatal(err)
	}
	// Flip a byte in the ciphertext body (past the header).
	b, _ := os.ReadFile(enc)
	b[fcHeaderSize+10] ^= 0x01
	os.WriteFile(enc, b, 0o600)
	if err := DecryptFile(key, enc, dec); err == nil {
		t.Fatal("decrypt accepted tampered ciphertext — must fail")
	}
}

func TestDecryptFileDetectsTruncation(t *testing.T) {
	dir := t.TempDir()
	key := testKey(t)
	// Multi-chunk input so dropping the tail removes the final chunk.
	src := writeTemp(t, "p", bytes.Repeat([]byte("A"), defaultChunkSize*2+50))
	enc := filepath.Join(dir, "enc")
	dec := filepath.Join(dir, "dec")
	if err := EncryptFile(key, src, enc); err != nil {
		t.Fatal(err)
	}
	// Chop off the last 100 bytes to drop/mangle the final chunk.
	b, _ := os.ReadFile(enc)
	os.WriteFile(enc, b[:len(b)-100], 0o600)
	if err := DecryptFile(key, enc, dec); err == nil {
		t.Fatal("decrypt accepted truncated file — must fail")
	}
}
