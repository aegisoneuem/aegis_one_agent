//go:build windows

package secure

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSealMachine_RoundTripAndFormatDistinct(t *testing.T) {
	data := []byte("mtls-client-private-key-PEM")
	blob, err := SealMachine(data)
	if err != nil {
		t.Fatalf("SealMachine: %v", err)
	}
	back, err := OpenMachine(blob)
	if err != nil || !bytes.Equal(back, data) {
		t.Fatalf("OpenMachine round-trip failed: %v", err)
	}
	// An AES-master-key-sealed blob must NOT open as DPAPI (so the migration path
	// in loadClientKeyPEM can tell the two formats apart safely).
	aesKey := make([]byte, KeySize)
	aesBlob, _ := Seal(aesKey, data)
	if _, err := OpenMachine(aesBlob); err == nil {
		t.Fatalf("OpenMachine must fail on an AES-sealed blob (format confusion)")
	}
}

// An existing client key sealed with the OLD AES master key is transparently
// decrypted AND migrated to DPAPI sealing on load, so it survives master-key loss
// from then on.
func TestLoadClientKeyPEM_MigratesAesToDpapi(t *testing.T) {
	dir := t.TempDir()
	keys := NewKeyProvider(dir)
	mk, err := keys.Key()
	if err != nil {
		t.Fatalf("master key: %v", err)
	}

	keyPath := filepath.Join(dir, "client.key")
	encPath := keyPath + ".enc"
	plain := []byte("-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----\n")

	oldBlob, err := Seal(mk, plain) // legacy AES-master-key sealing
	if err != nil {
		t.Fatalf("seal legacy: %v", err)
	}
	if err := os.WriteFile(encPath, oldBlob, 0o600); err != nil {
		t.Fatalf("write enc: %v", err)
	}

	got, err := loadClientKeyPEM(keys, keyPath)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("load failed: err=%v match=%v", err, bytes.Equal(got, plain))
	}

	// After load the on-disk blob must now be DPAPI-sealed (survives master-key loss).
	migrated, _ := os.ReadFile(encPath)
	if _, derr := OpenMachine(migrated); derr != nil {
		t.Fatalf("client key not migrated to DPAPI sealing: %v", derr)
	}
}

// A freshly protected key is DPAPI-sealed and does NOT need the master key to load.
func TestProtectKeyAtRest_DpapiSealed_NoMasterKeyNeeded(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "client.key")
	plain := []byte("fresh-client-key")
	if err := os.WriteFile(keyPath, plain, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	if err := protectKeyAtRest(nil, keyPath); err != nil {
		t.Fatalf("protect: %v", err)
	}
	// Load with a NIL key provider (no master key at all) — DPAPI alone must suffice.
	got, err := loadClientKeyPEM(nil, keyPath)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("load without master key failed: err=%v match=%v", err, bytes.Equal(got, plain))
	}
}
