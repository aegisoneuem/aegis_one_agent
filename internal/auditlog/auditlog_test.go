package auditlog

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestAppendBasic — one append writes a JSONL line with a timestamp.
func TestAppendBasic(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := l.Append(Event{
		DeviceID: "dev-1", EventType: "command_received",
		CommandID: "c-1", Decision: "accepted",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ev.TS == 0 {
		t.Error("TS not stamped")
	}
	data, err := os.ReadFile(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"command_id":"c-1"`) {
		t.Errorf("appended line missing command_id: %s", data)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Error("appended line missing trailing newline")
	}
}

// TestRotation — rotating preserves old file, starts fresh.
func TestRotation(t *testing.T) {
	dir := t.TempDir()
	l, err := NewWithLimit(dir, 200) // tiny so a couple events trigger rotate
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := l.Append(Event{
			DeviceID: "dev-r", EventType: "command_received",
			CommandID: "c", Decision: "accepted",
		}); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	rotated := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "audit-") && strings.HasSuffix(e.Name(), ".log") {
			rotated++
		}
	}
	if rotated < 1 {
		t.Errorf("expected at least 1 rotated file; found %d (entries: %v)", rotated, entries)
	}
}

// TestRead — Read returns all events oldest-first.
func TestRead(t *testing.T) {
	dir := t.TempDir()
	l, _ := New(dir)
	ids := []string{"a", "b", "c"}
	for _, id := range ids {
		_, _ = l.Append(Event{DeviceID: "d", EventType: "command_received", CommandID: id, Decision: "accepted"})
	}
	evs, err := Read(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != len(ids) {
		t.Fatalf("Read returned %d events, want %d", len(evs), len(ids))
	}
	for i, ev := range evs {
		if ev.CommandID != ids[i] {
			t.Errorf("event %d id=%q want %q", i, ev.CommandID, ids[i])
		}
	}
}

// TestConcurrentAppend — 50 goroutines × 10 events each; no corruption.
func TestConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	l, _ := New(dir)
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				_, _ = l.Append(Event{
					DeviceID: "dev", EventType: "command_received",
					CommandID: "c", Decision: "accepted",
				})
			}
		}(g)
	}
	wg.Wait()
	evs, err := Read(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 500 {
		t.Errorf("got %d events, want 500", len(evs))
	}
}

// Break: append after rotation still writes to the fresh file.
func TestBreak_AppendAfterRotation(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewWithLimit(dir, 200)
	for i := 0; i < 8; i++ {
		_, _ = l.Append(Event{DeviceID: "d", EventType: "command_received", Decision: "accepted"})
	}
	data, err := os.ReadFile(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Error("current audit.log is empty after rotation sequence")
	}
}

// Break: non-existent dir is created with mkdir.
func TestBreak_MkdirMissingParent(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	l, err := New(deep)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(Event{DeviceID: "d", EventType: "x", Decision: "accepted"}); err != nil {
		t.Error(err)
	}
}

// Break: Read on empty file returns empty slice, not error.
func TestBreak_ReadEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	evs, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Errorf("Read empty file returned %d events, want 0", len(evs))
	}
}
