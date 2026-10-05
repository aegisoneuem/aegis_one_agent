// Package patchcache is the agent's encrypted, size-bounded store for
// server-delivered patch/package files. Files are held encrypted at rest
// (AES-256-GCM via internal/secure) under the agent's secure dir and decrypted
// only when an install needs them.
//
// Eviction respects three states:
//   - staged:    pre-cached for a pending/scheduled deployment — never evicted or
//     auto-deleted until its deployment completes.
//   - cached:    normal — evictable by LRU when over the size cap, and by TTL
//     (unused for longer than the TTL, default 24h).
//   - installed: install finished — eligible for cleanup.
//
// When a new file cannot fit and nothing evictable remains (everything left is
// staged), Put returns ErrCacheFull so the caller can notify the server
// ("increase cache disk space or run the cleanup task").
package patchcache

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"crypto/sha256"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
)

// ErrCacheFull is returned when a file cannot be cached because the size cap is
// reached and no non-staged entries can be evicted to make room.
var ErrCacheFull = errors.New("patch cache full")

// State of a cached entry (see package doc).
type State string

const (
	StateCached    State = "cached"
	StateStaged    State = "staged"
	StateInstalled State = "installed"
)

// DefaultMaxBytes is the baseline cache cap (2 GiB); admins raise it as needed.
// A single monthly cumulative update can approach this, so the active install
// path must not be blocked by the cap (see B2b: JIT installs bypass the cap).
const DefaultMaxBytes int64 = 2 << 30

// DefaultTTL is how long an unused cached file is kept before TTL cleanup.
const DefaultTTL = 24 * time.Hour

type entry struct {
	ID         string `json:"id"`
	File       string `json:"file"` // encrypted file name within the cache dir
	Ext        string `json:"ext"`  // original extension (e.g. ".msu"/".cab") for decrypt-to-temp
	Size       int64  `json:"size"` // on-disk (encrypted) size, for cap accounting
	State      State  `json:"state"`
	LastAccess int64  `json:"last_access"` // unix seconds
	CreatedAt  int64  `json:"created_at"`
}

// Cache is a concurrency-safe encrypted patch cache.
type Cache struct {
	dir      string
	index    string
	keys     secure.KeyProvider
	maxBytes int64
	ttl      time.Duration

	mu      sync.Mutex
	entries map[string]*entry
	now     func() time.Time // injectable clock for tests
}

// New opens (or creates) a cache under dir. maxBytes<=0 uses DefaultMaxBytes;
// ttl<=0 uses DefaultTTL.
func New(dir string, keys secure.KeyProvider, maxBytes int64, ttl time.Duration) (*Cache, error) {
	if keys == nil {
		return nil, errors.New("patchcache requires a key provider")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir %q: %w", dir, err)
	}
	c := &Cache{
		dir:      dir,
		index:    filepath.Join(dir, "index.json"),
		keys:     keys,
		maxBytes: maxBytes,
		ttl:      ttl,
		entries:  make(map[string]*entry),
		now:      time.Now,
	}
	if err := c.load(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Cache) fileName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:16]) + ".enc"
}

func (c *Cache) load() error {
	data, err := os.ReadFile(c.index)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read index: %w", err)
	}
	var list []*entry
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("parse index: %w", err)
	}
	for _, e := range list {
		c.entries[e.ID] = e
	}
	return nil
}

func (c *Cache) save() error {
	list := make([]*entry, 0, len(c.entries))
	for _, e := range c.entries {
		list = append(list, e)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.index + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.index)
}

func (c *Cache) totalBytes() int64 {
	var t int64
	for _, e := range c.entries {
		t += e.Size
	}
	return t
}

// Put encrypts srcPath into the cache under id with the given state. If it would
// exceed the cap, it first evicts TTL-expired and then LRU non-staged entries;
// if it still cannot fit, it returns ErrCacheFull and stores nothing.
func (c *Cache) Put(id, srcPath string, state State) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	info, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("stat source %q: %w", srcPath, err)
	}
	need := info.Size() // ciphertext is ~plaintext + small overhead; close enough for the cap

	// If replacing an existing id, free its current size from the running total.
	existing := int64(0)
	if e, ok := c.entries[id]; ok {
		existing = e.Size
	}

	c.evictExpiredLocked()
	if c.totalBytes()-existing+need > c.maxBytes {
		c.evictToFitLocked(need+existing, id)
	}
	if c.totalBytes()-existing+need > c.maxBytes {
		// Persist any evictions we did make before reporting the cache is full.
		_ = c.save()
		return fmt.Errorf("%w: need %d bytes, cap %d, in use %d", ErrCacheFull, need, c.maxBytes, c.totalBytes())
	}

	key, err := c.keys.Key()
	if err != nil {
		return fmt.Errorf("get key: %w", err)
	}
	fname := c.fileName(id)
	dst := filepath.Join(c.dir, fname)
	if err := secure.EncryptFile(key, srcPath, dst); err != nil {
		return fmt.Errorf("encrypt into cache: %w", err)
	}
	encInfo, err := os.Stat(dst)
	if err != nil {
		return fmt.Errorf("stat cached file: %w", err)
	}
	now := c.now().Unix()
	c.entries[id] = &entry{
		ID:         id,
		File:       fname,
		Ext:        filepath.Ext(srcPath),
		Size:       encInfo.Size(),
		State:      state,
		LastAccess: now,
		CreatedAt:  now,
	}
	return c.save()
}

// Decrypt writes the plaintext of a cached entry to destPath and refreshes its
// last-access time. The caller is responsible for securely removing destPath
// after use (it is plaintext).
func (c *Cache) Decrypt(id, destPath string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[id]
	if !ok {
		return fmt.Errorf("cache miss for id %q", id)
	}
	key, err := c.keys.Key()
	if err != nil {
		return fmt.Errorf("get key: %w", err)
	}
	if err := secure.DecryptFile(key, filepath.Join(c.dir, e.File), destPath); err != nil {
		return fmt.Errorf("decrypt cached file: %w", err)
	}
	e.LastAccess = c.now().Unix()
	return c.save()
}

// SetState changes an entry's state (e.g. mark staged→installed after install).
func (c *Cache) SetState(id string, state State) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if !ok {
		return fmt.Errorf("cache miss for id %q", id)
	}
	e.State = state
	return c.save()
}

// Remove deletes an entry and its file.
func (c *Cache) Remove(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.removeLocked(id)
}

func (c *Cache) removeLocked(id string) error {
	e, ok := c.entries[id]
	if !ok {
		return nil
	}
	if err := os.Remove(filepath.Join(c.dir, e.File)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove cached file: %w", err)
	}
	delete(c.entries, id)
	return c.save()
}

// EntryInfo is a read-only snapshot of a cached entry (for listing/diagnostics).
type EntryInfo struct {
	ID         string
	State      State
	Size       int64
	LastAccess int64
}

// List returns a snapshot of all cached entries.
func (c *Cache) List() []EntryInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]EntryInfo, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, EntryInfo{ID: e.ID, State: e.State, Size: e.Size, LastAccess: e.LastAccess})
	}
	return out
}

// Usage returns the bytes currently cached and the configured cap, for
// cache-full reporting and diagnostics.
func (c *Cache) Usage() (inUse, max int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totalBytes(), c.maxBytes
}

// Has reports whether an id is cached.
func (c *Cache) Has(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[id]
	return ok
}

// Ext returns the original file extension stored for a cached entry (e.g.
// ".msu"), so a decrypt-to-temp keeps the extension DISM expects.
func (c *Cache) Ext(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[id]; ok {
		return e.Ext
	}
	return ""
}

// Enforce runs TTL cleanup then LRU cap eviction. It returns ErrCacheFull if the
// cache is still over the cap afterwards (i.e. only staged entries remain).
func (c *Cache) Enforce() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictExpiredLocked()
	if c.totalBytes() > c.maxBytes {
		c.evictToFitLocked(0, "")
	}
	if err := c.save(); err != nil {
		return err
	}
	if c.totalBytes() > c.maxBytes {
		return fmt.Errorf("%w: in use %d, cap %d", ErrCacheFull, c.totalBytes(), c.maxBytes)
	}
	return nil
}

// evictExpiredLocked deletes non-staged entries unused for longer than the TTL.
func (c *Cache) evictExpiredLocked() {
	cutoff := c.now().Add(-c.ttl).Unix()
	for id, e := range c.entries {
		if e.State == StateStaged {
			continue
		}
		if e.LastAccess < cutoff {
			_ = c.removeFileLocked(id)
		}
	}
}

// evictToFitLocked removes non-staged entries in LRU order until the cache would
// have room for `need` more bytes, skipping keepID. Staged entries are never
// touched, so it may not free enough.
func (c *Cache) evictToFitLocked(need int64, keepID string) {
	type cand struct {
		id         string
		lastAccess int64
	}
	var cands []cand
	for id, e := range c.entries {
		if e.State == StateStaged || id == keepID {
			continue
		}
		cands = append(cands, cand{id: id, lastAccess: e.LastAccess})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].lastAccess < cands[j].lastAccess })

	for _, cd := range cands {
		if c.totalBytes()+need <= c.maxBytes {
			return
		}
		_ = c.removeFileLocked(cd.id)
	}
}

// removeFileLocked deletes the file and entry without persisting the index
// (callers persist once at the end).
func (c *Cache) removeFileLocked(id string) error {
	e, ok := c.entries[id]
	if !ok {
		return nil
	}
	if err := os.Remove(filepath.Join(c.dir, e.File)); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(c.entries, id)
	return nil
}
