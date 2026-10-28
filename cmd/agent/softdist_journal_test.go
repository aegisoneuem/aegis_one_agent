package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestJournalWriteReadDelete exercises the basic durable record lifecycle: a
// transition writes a self-describing entry, read returns it, and delete
// removes it. Missing-on-delete is not an error.
func TestJournalWriteReadDelete(t *testing.T) {
	j := newJobJournal(t.TempDir())
	if err := j.write(&journalEntry{CommandID: "deploy-1", DeviceID: "dev", State: journalRunning, Name: "x", StartedAt: 100}); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	got, err := j.read("deploy-1")
	if err != nil || got == nil {
		t.Fatalf("read deploy-1 returned %+v err=%v", got, err)
	}
	if got.State != journalRunning || got.Name != "x" || got.LastUpdated == 0 {
		t.Fatalf("unexpected read result: %+v", got)
	}
	if err := j.delete("deploy-1"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if err := j.delete("deploy-1"); err != nil {
		t.Fatalf("second delete should be a no-op: %v", err)
	}
	if got, err := j.read("deploy-1"); err != nil || got != nil {
		t.Fatalf("expected deleted record missing, got=%+v err=%v", got, err)
	}
}

// TestJournalRecoverListsInflight: entries not reaching a terminal state are
// returned so the installer can emit RECOVERED_FAILED for them.
func TestJournalRecoverListsInflight(t *testing.T) {
	j := newJobJournal(t.TempDir())
	entries := []*journalEntry{
		{CommandID: "a", State: journalQueued, Name: "A", StartedAt: 1},
		{CommandID: "b", State: journalRunning, Name: "B", StartedAt: 2, PackageDir: `C:\x\y`},
		{CommandID: "c", State: journalVerifying, Name: "C", StartedAt: 3},
	}
	for _, e := range entries {
		if err := j.write(e); err != nil {
			t.Fatalf("write %s: %v", e.CommandID, err)
		}
	}
	got, err := j.recover()
	if err != nil {
		t.Fatalf("recover failed: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 recovered entries, got %d: %+v", len(got), got)
	}
	seen := map[string]bool{}
	for _, e := range got {
		seen[e.CommandID] = true
	}
	for _, id := range []string{"a", "b", "c"} {
		if !seen[id] {
			t.Fatalf("missing recovered entry %s", id)
		}
	}
}

// TestJournalRecoverSkipsCorruptAndTmp: a half-written tmp file and a corrupt
// JSON file must not block the sweep — both are removed and real entries
// still return.
func TestJournalRecoverSkipsCorruptAndTmp(t *testing.T) {
	root := t.TempDir()
	j := newJobJournal(root)
	if err := j.write(&journalEntry{CommandID: "good", State: journalRunning, StartedAt: 1}); err != nil {
		t.Fatalf("write good: %v", err)
	}
	// Half-written tmp (recovery removes it).
	tmpPath := filepath.Join(j.dir, "stale.state.tmp")
	if err := os.WriteFile(tmpPath, []byte("partial"), 0o600); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	// Corrupt JSON (recovery removes it).
	corruptPath := filepath.Join(j.dir, "corrupt.state")
	if err := os.WriteFile(corruptPath, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	got, err := j.recover()
	if err != nil {
		t.Fatalf("recover failed: %v", err)
	}
	if len(got) != 1 || got[0].CommandID != "good" {
		t.Fatalf("expected only 'good' entry, got %+v", got)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("tmp file should have been removed")
	}
	if _, err := os.Stat(corruptPath); !os.IsNotExist(err) {
		t.Fatalf("corrupt file should have been removed")
	}
}

// TestJournalAtomicRenameNoHalfRead: a concurrent read during a write must
// see either the previous version or the new one — never a partial file.
// The write uses write-tmp + rename, so this is really a guard against
// anyone changing that strategy later.
func TestJournalAtomicRenameNoHalfRead(t *testing.T) {
	j := newJobJournal(t.TempDir())
	if err := j.write(&journalEntry{CommandID: "p", State: journalRunning, StartedAt: 1}); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	// Overwrite with a MUCH larger entry: if write were naive (truncate+write
	// in place), a concurrent read could get a short/empty file.
	big := &journalEntry{CommandID: "p", State: journalRunning, StartedAt: 1}
	big.Name = string(make([]byte, 64*1024))
	if err := j.write(big); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	data, err := os.ReadFile(j.path("p"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var round journalEntry
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatalf("round-trip JSON: %v, data head=%q", err, string(data[:min(200, len(data))]))
	}
	if len(round.Name) != 64*1024 {
		t.Fatalf("expected 64KiB name, got %d", len(round.Name))
	}
}

func TestRecoverCleanupPackageDirRefusesEscape(t *testing.T) {
	downloadDir := t.TempDir()
	packagesRoot := filepath.Join(downloadDir, "packages")
	if err := os.MkdirAll(packagesRoot, 0o755); err != nil {
		t.Fatalf("mkdir packages: %v", err)
	}
	// Legitimate: packages/aegis-package-xyz — must be removable.
	legit := filepath.Join(packagesRoot, "aegis-package-xyz")
	if err := os.MkdirAll(legit, 0o755); err != nil {
		t.Fatalf("mkdir legit: %v", err)
	}
	if err := recoverCleanupPackageDir(legit, downloadDir); err != nil {
		t.Fatalf("expected legit cleanup to succeed, got %v", err)
	}
	if _, err := os.Stat(legit); !os.IsNotExist(err) {
		t.Fatalf("legit extraction dir not removed")
	}
	// Hostile: an arbitrary absolute path outside packages root — must be refused.
	outside := filepath.Join(downloadDir, "evil")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := recoverCleanupPackageDir(outside, downloadDir); err == nil {
		t.Fatalf("expected refusal for non-package path, got nil error")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside path should NOT have been removed: %v", err)
	}
	// Vanished dir: already gone is fine, not an error.
	if err := recoverCleanupPackageDir(filepath.Join(packagesRoot, "gone"), downloadDir); err != nil {
		t.Fatalf("expected vanished dir to be ok, got %v", err)
	}
}
