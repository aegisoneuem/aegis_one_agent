package auditlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestAppendBasic — happy path: 3 events appended, each has prev_hash =
// previous line_hash, first line has empty prev_hash.
func TestAppendBasic(t *testing.T) {
	tmp := t.TempDir()
	l, err := New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	e1, err := l.Append(Event{DeviceID: "dev-1", EventType: "command_received", CommandID: "c1", Decision: "accepted", SignatureOK: true})
	if err != nil {
		t.Fatal(err)
	}
	if e1.PrevHash != "" {
		t.Errorf("first event prev_hash = %q, want empty", e1.PrevHash)
	}
	if e1.LineHash == "" {
		t.Error("first event line_hash empty")
	}
	e2, _ := l.Append(Event{DeviceID: "dev-1", EventType: "command_received", CommandID: "c2", Decision: "accepted", SignatureOK: true})
	if e2.PrevHash != e1.LineHash {
		t.Errorf("event 2 prev_hash = %q, want %q", e2.PrevHash, e1.LineHash)
	}
	e3, _ := l.Append(Event{DeviceID: "dev-1", EventType: "command_rejected", CommandID: "c3", Decision: "rejected", SignatureOK: false, Reason: "bad signature"})
	if e3.PrevHash != e2.LineHash {
		t.Errorf("event 3 prev_hash = %q, want %q", e3.PrevHash, e2.LineHash)
	}
	// Chain verification passes.
	idx, reason, err := VerifyChain(filepath.Join(tmp, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if idx != -1 {
		t.Errorf("chain broken at line %d: %s", idx, reason)
	}
}

// TestRotationCreatesIndependentFile — fill to size cap, confirm
// rotation happens AND each file is independently verifiable (new file
// starts fresh at prev_hash=""). Cross-file continuity lives on the
// server side via streamed events — intentional trade-off.
func TestRotationCreatesIndependentFile(t *testing.T) {
	tmp := t.TempDir()
	l, err := NewWithLimit(tmp, 512) // force rotation after a few events
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		_, err := l.Append(Event{DeviceID: "dev-1", EventType: "command_received", CommandID: fmt.Sprintf("c%d", i), Decision: "accepted", SignatureOK: true})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	// At least one rotated file should exist.
	matches, _ := filepath.Glob(filepath.Join(tmp, "audit-*.log"))
	if len(matches) == 0 {
		t.Fatal("no rotated files produced — size cap not triggered")
	}
	// Both the rotated AND current file independently verify.
	for _, p := range matches {
		idx, reason, err := VerifyChain(p)
		if err != nil {
			t.Fatalf("verify %s: %v", p, err)
		}
		if idx != -1 {
			t.Errorf("rotated file %s broken at line %d: %s", p, idx, reason)
		}
	}
	idx, reason, err := VerifyChain(filepath.Join(tmp, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if idx != -1 {
		t.Errorf("current file broken at line %d: %s", idx, reason)
	}
}

// TestVerifyChain_DetectsEdit — append 3 events, manually edit one on
// disk, confirm VerifyChain flags the tamper.
func TestVerifyChain_DetectsEdit(t *testing.T) {
	tmp := t.TempDir()
	l, _ := New(tmp)
	for i := 0; i < 3; i++ {
		_, _ = l.Append(Event{DeviceID: "dev-1", EventType: "command_received", CommandID: fmt.Sprintf("c%d", i), Decision: "accepted", SignatureOK: true})
	}
	path := filepath.Join(tmp, "audit.log")
	data, _ := os.ReadFile(path)
	// Flip one character in line 2 — enough to break the hash.
	// Find the "reason" field and inject a byte.
	tampered := strings.Replace(string(data), `"c1"`, `"CZ"`, 1)
	if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	idx, reason, err := VerifyChain(path)
	if err != nil {
		t.Fatal(err)
	}
	if idx == -1 {
		t.Error("VerifyChain did NOT detect manual edit — tamper evidence failed")
	}
	if !strings.Contains(reason, "line_hash") && !strings.Contains(reason, "prev_hash") {
		t.Errorf("reason unclear: %s", reason)
	}
}

// TestConcurrentAppend — 100 goroutines x 10 events each. Chain must
// remain intact (one-writer serialization via Logger.mu).
func TestConcurrentAppend(t *testing.T) {
	tmp := t.TempDir()
	l, _ := New(tmp)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if _, err := l.Append(Event{DeviceID: "dev-1", EventType: "command_received", CommandID: fmt.Sprintf("w%d-c%d", worker, j), Decision: "accepted", SignatureOK: true}); err != nil {
					t.Errorf("concurrent append: %v", err)
				}
			}
		}(i)
	}
	wg.Wait()
	// All 1000 events written, chain intact.
	data, _ := os.ReadFile(filepath.Join(tmp, "audit.log"))
	lines := splitLines(data)
	if len(lines) != 1000 {
		t.Errorf("wrote %d lines, want 1000", len(lines))
	}
	idx, reason, err := VerifyChain(filepath.Join(tmp, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if idx != -1 {
		t.Errorf("concurrent writes broke chain at line %d: %s", idx, reason)
	}
}

// TestWriteFailure_ChainStateUnchanged — simulate a write failure (file
// opened read-only) and confirm the chain state does NOT advance so the
// next successful Append retries cleanly.
func TestWriteFailure_ChainStateUnchanged(t *testing.T) {
	tmp := t.TempDir()
	l, _ := New(tmp)
	e1, _ := l.Append(Event{DeviceID: "dev-1", EventType: "command_received", CommandID: "c1", Decision: "accepted", SignatureOK: true})
	firstChainHead := l.lastHash
	if e1.LineHash != firstChainHead {
		t.Fatalf("setup: lastHash mismatch")
	}
	// Point Logger at a path we cannot write to.
	l.path = filepath.Join(tmp, "no-such-dir", "audit.log")
	_, err := l.Append(Event{DeviceID: "dev-1", EventType: "command_received", CommandID: "c2", Decision: "accepted", SignatureOK: true})
	if err == nil {
		t.Fatal("expected write failure")
	}
	if l.lastHash != firstChainHead {
		t.Errorf("chain head advanced despite write failure: %q, want %q", l.lastHash, firstChainHead)
	}
	// Point back at the real file; next Append should still chain from c1.
	l.path = filepath.Join(tmp, "audit.log")
	e3, err := l.Append(Event{DeviceID: "dev-1", EventType: "command_received", CommandID: "c3", Decision: "accepted", SignatureOK: true})
	if err != nil {
		t.Fatal(err)
	}
	if e3.PrevHash != firstChainHead {
		t.Errorf("after recovery prev_hash = %q, want %q", e3.PrevHash, firstChainHead)
	}
}

// TestRejectedCommandsAreLogged — bad-signature commands produce their
// own record with signature_ok=false, decision="rejected".
func TestRejectedCommandsAreLogged(t *testing.T) {
	tmp := t.TempDir()
	l, _ := New(tmp)
	_, err := l.Append(Event{DeviceID: "dev-1", EventType: "command_rejected", CommandID: "bad-1", Decision: "rejected", SignatureOK: false, Reason: "bad signature"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(tmp, "audit.log"))
	var ev Event
	if err := json.Unmarshal(splitLines(data)[0], &ev); err != nil {
		t.Fatal(err)
	}
	if ev.SignatureOK {
		t.Error("rejected event logged with signature_ok=true")
	}
	if ev.Decision != "rejected" {
		t.Errorf("decision = %q, want rejected", ev.Decision)
	}
}

// TestCanonicalJSONStable — the same Event always serializes identically
// so the hash is reproducible.
func TestCanonicalJSONStable(t *testing.T) {
	ev := Event{DeviceID: "d", TS: 123, EventType: "e", CommandID: "c", CommandType: "t", Actor: "a", Source: "s", IssuedAt: 456, SignatureOK: true, Decision: "accepted", Reason: "r", PrevHash: "p", LineHash: "lh"}
	a, _ := canonicalJSON(ev)
	b, _ := canonicalJSON(ev)
	if string(a) != string(b) {
		t.Errorf("canonicalJSON not stable:\n%s\n%s", a, b)
	}
}

// TestTailHashAcrossRestart — write 2 events, close, reopen, append 1
// more. Third event's prev_hash must equal second's line_hash.
func TestTailHashAcrossRestart(t *testing.T) {
	tmp := t.TempDir()
	l1, _ := New(tmp)
	_, _ = l1.Append(Event{DeviceID: "d1", EventType: "command_received", CommandID: "c1", Decision: "accepted", SignatureOK: true})
	e2, _ := l1.Append(Event{DeviceID: "d1", EventType: "command_received", CommandID: "c2", Decision: "accepted", SignatureOK: true})
	// Simulate restart: create a NEW Logger pointing at the same dir.
	l2, err := New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	e3, _ := l2.Append(Event{DeviceID: "d1", EventType: "command_received", CommandID: "c3", Decision: "accepted", SignatureOK: true})
	if e3.PrevHash != e2.LineHash {
		t.Errorf("post-restart chain broken: prev=%q, want %q", e3.PrevHash, e2.LineHash)
	}
}
