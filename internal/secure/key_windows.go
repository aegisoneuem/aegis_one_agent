//go:build windows

package secure

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// keyFileName is the DPAPI-protected master key blob, stored under the agent's
// secure directory (e.g. C:\Program Files\AegisOne\client\SKVC).
const keyFileName = "dpapi.key"

// appEntropy binds the DPAPI blob to this application. It is not a secret (it
// ships in the binary) — it is a salt so the protected key cannot be
// unprotected by unrelated machine-scope DPAPI callers.
var appEntropy = []byte("AegisOne.client.SKVC.master-key.v1")

// dpapiKeyProvider loads (or creates on first use) an AES-256 master key kept
// on disk only in DPAPI-protected form. The unprotected key lives in memory.
type dpapiKeyProvider struct {
	path string
	mu   sync.Mutex
	key  []byte
}

// NewKeyProvider returns a DPAPI-backed KeyProvider that stores its master key
// at dir/dpapi.key. The key is created on first Key() call if absent.
func NewKeyProvider(dir string) KeyProvider {
	return &dpapiKeyProvider{path: filepath.Join(dir, keyFileName)}
}

func (p *dpapiKeyProvider) Key() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.key != nil {
		return p.key, nil
	}

	// Existing key: read the protected blob and unprotect it.
	if blob, err := os.ReadFile(p.path); err == nil {
		key, err := dpapiUnprotect(blob, appEntropy)
		if err != nil {
			return nil, fmt.Errorf("unprotect master key %q: %w", p.path, err)
		}
		if len(key) != KeySize {
			return nil, fmt.Errorf("master key at %q has wrong size %d (want %d)", p.path, len(key), KeySize)
		}
		p.key = key
		return key, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read master key %q: %w", p.path, err)
	}

	// First run: generate a fresh key, protect it, and persist the blob.
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	blob, err := dpapiProtect(key, appEntropy)
	if err != nil {
		return nil, fmt.Errorf("protect master key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		return nil, fmt.Errorf("create key directory: %w", err)
	}
	if err := os.WriteFile(p.path, blob, 0o600); err != nil {
		return nil, fmt.Errorf("write master key %q: %w", p.path, err)
	}
	p.key = key
	return key, nil
}
