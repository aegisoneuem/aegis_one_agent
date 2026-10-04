package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// baselineStore persists the server-pushed patch baseline ("for your build, the
// latest known KBs are …") so the agent can run an offline self-diff. It is
// plaintext (KB numbers are public, not sensitive) and survives restarts.
type baselineStore struct {
	path string
	mu   sync.Mutex
}

type cachedBaseline struct {
	Build     string   `json:"build"`
	LatestKBs []string `json:"latest_kbs"`
	IssuedAt  int64    `json:"issued_at"`
}

func newBaselineStore(dir string) *baselineStore {
	return &baselineStore{path: filepath.Join(dir, "baseline.json")}
}

func (b *baselineStore) save(bl *pb.PatchBaseline) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, err := json.MarshalIndent(cachedBaseline{
		Build:     bl.Build,
		LatestKBs: bl.LatestKbs,
		IssuedAt:  bl.IssuedAt,
	}, "", "  ")
	if err != nil {
		return err
	}
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, b.path)
}

// load returns the cached baseline, or nil (no error) when none has been cached.
func (b *baselineStore) load() (*cachedBaseline, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, err := os.ReadFile(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var c cachedBaseline
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// normalizeKB strips an optional "KB" prefix and lowercases, so baseline and
// installed lists compare regardless of formatting.
func normalizeKB(kb string) string {
	kb = strings.ToLower(strings.TrimSpace(kb))
	kb = strings.TrimPrefix(kb, "kb")
	return kb
}
