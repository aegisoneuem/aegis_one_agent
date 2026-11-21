//go:build linux

package secure

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Linux master-key provider. Windows protects the AES-256 master key with DPAPI; on
// Linux the key is stored raw in a root-only (0600) file under the data dir. The
// filesystem permission is the protection — equivalent threat model to DPAPI machine
// scope (a local root/admin can read it either way). A stronger store (kernel keyring
// / TPM seal) is a later hardening.
type fileKeyProvider struct {
	path string
	mu   sync.Mutex
	key  []byte
}

func NewKeyProvider(dir string) KeyProvider {
	return &fileKeyProvider{path: filepath.Join(dir, KeyFileName)}
}

func (p *fileKeyProvider) Key() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.key != nil {
		return p.key, nil
	}
	if b, err := os.ReadFile(p.path); err == nil {
		if len(b) != KeySize {
			return nil, fmt.Errorf("master key at %q has wrong size %d (want %d)", p.path, len(b), KeySize)
		}
		p.key = b
		return b, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read master key %q: %w", p.path, err)
	}
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return nil, fmt.Errorf("create key directory: %w", err)
	}
	if err := os.WriteFile(p.path, key, 0o600); err != nil {
		return nil, fmt.Errorf("write master key %q: %w", p.path, err)
	}
	p.key = key
	return key, nil
}
