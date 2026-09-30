package patchcache

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// staticKey is a fixed-key provider so tests need no DPAPI and run everywhere.
type staticKey struct{}

func (staticKey) Key() ([]byte, error) {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 7)
	}
	return k, nil
}

type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time { return f.t }

func srcFile(t *testing.T, dir, name string, size int) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, bytes.Repeat([]byte("p"), size), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func newCache(t *testing.T, maxBytes int64, ttl time.Duration) (*Cache, *fakeClock, string) {
	t.Helper()
	dir := t.TempDir()
	c, err := New(dir, staticKey{}, maxBytes, ttl)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	c.now = clk.now
	return c, clk, dir
}

func TestCachePutDecryptRoundTrip(t *testing.T) {
	c, _, dir := newCache(t, 0, 0)
	src := srcFile(t, dir, "patch.msu", 500)
	want, _ := os.ReadFile(src)

	if err := c.Put("KB1", src, StateCached); err != nil {
		t.Fatalf("put: %v", err)
	}
	if !c.Has("KB1") {
		t.Fatal("Has(KB1) false after Put")
	}

	// On-disk cached file must be ciphertext (no plaintext run).
	files, _ := filepath.Glob(filepath.Join(dir, "*.enc"))
	if len(files) != 1 {
		t.Fatalf("expected 1 .enc file, got %d", len(files))
	}
	enc, _ := os.ReadFile(files[0])
	if bytes.Contains(enc, bytes.Repeat([]byte("p"), 32)) {
		t.Fatal("plaintext found in cached file")
	}

	out := filepath.Join(dir, "out.msu")
	if err := c.Decrypt("KB1", out); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	got, _ := os.ReadFile(out)
	if !bytes.Equal(got, want) {
		t.Fatal("decrypted content mismatch")
	}
}

func TestCachePersistsAcrossReopen(t *testing.T) {
	c, _, dir := newCache(t, 0, 0)
	src := srcFile(t, dir, "p", 200)
	if err := c.Put("KB1", src, StateStaged); err != nil {
		t.Fatal(err)
	}

	c2, err := New(dir, staticKey{}, 0, 0)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !c2.Has("KB1") {
		t.Fatal("entry did not survive reopen")
	}
}

func TestCacheLRUEvictionUnderCap(t *testing.T) {
	// Cap fits two ~240-byte encrypted files but not three.
	c, clk, dir := newCache(t, 520, time.Hour)

	if err := c.Put("A", srcFile(t, dir, "a", 200), StateCached); err != nil {
		t.Fatalf("put A: %v", err)
	}
	clk.t = clk.t.Add(time.Minute)
	if err := c.Put("B", srcFile(t, dir, "b", 200), StateCached); err != nil {
		t.Fatalf("put B: %v", err)
	}
	// Touch A so B becomes the least-recently-used.
	clk.t = clk.t.Add(time.Minute)
	if err := c.Decrypt("A", filepath.Join(dir, "a.out")); err != nil {
		t.Fatalf("touch A: %v", err)
	}
	clk.t = clk.t.Add(time.Minute)
	if err := c.Put("C", srcFile(t, dir, "c", 200), StateCached); err != nil {
		t.Fatalf("put C: %v", err)
	}

	if !c.Has("A") || !c.Has("C") {
		t.Fatal("A and C should remain")
	}
	if c.Has("B") {
		t.Fatal("B (LRU) should have been evicted")
	}
}

func TestCacheStagedNotEvictedAndReportsFull(t *testing.T) {
	c, clk, dir := newCache(t, 520, time.Hour)
	if err := c.Put("A", srcFile(t, dir, "a", 200), StateStaged); err != nil {
		t.Fatalf("put A: %v", err)
	}
	clk.t = clk.t.Add(time.Minute)
	if err := c.Put("B", srcFile(t, dir, "b", 200), StateStaged); err != nil {
		t.Fatalf("put B: %v", err)
	}
	// Third put cannot evict either staged entry → ErrCacheFull.
	clk.t = clk.t.Add(time.Minute)
	err := c.Put("C", srcFile(t, dir, "c", 200), StateCached)
	if err == nil {
		t.Fatal("expected ErrCacheFull")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("patch cache full")) {
		t.Fatalf("expected cache-full error, got %v", err)
	}
	if c.Has("C") {
		t.Fatal("C must not be stored when the cache is full")
	}
	if !c.Has("A") || !c.Has("B") {
		t.Fatal("staged entries must survive")
	}
}

func TestCacheTTLEviction(t *testing.T) {
	c, clk, dir := newCache(t, 0, time.Hour)
	if err := c.Put("old", srcFile(t, dir, "o", 100), StateCached); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("keep", srcFile(t, dir, "k", 100), StateStaged); err != nil {
		t.Fatal(err)
	}

	// Advance past the TTL and enforce.
	clk.t = clk.t.Add(2 * time.Hour)
	if err := c.Enforce(); err != nil {
		t.Fatalf("enforce: %v", err)
	}
	if c.Has("old") {
		t.Fatal("expired cached entry should be evicted by TTL")
	}
	if !c.Has("keep") {
		t.Fatal("staged entry must not be TTL-evicted")
	}
}

func TestCacheSetStateAndRemove(t *testing.T) {
	c, _, dir := newCache(t, 0, 0)
	if err := c.Put("KB", srcFile(t, dir, "p", 100), StateStaged); err != nil {
		t.Fatal(err)
	}
	if err := c.SetState("KB", StateInstalled); err != nil {
		t.Fatalf("setstate: %v", err)
	}
	if err := c.Remove("KB"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if c.Has("KB") {
		t.Fatal("entry should be gone after Remove")
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.enc")); len(files) != 0 {
		t.Fatal("cached file should be deleted after Remove")
	}
}
