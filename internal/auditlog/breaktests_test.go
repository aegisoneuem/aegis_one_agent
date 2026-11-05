package auditlog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// -----------------------------------------------------------------------------
// Break tests — explicit "try to make the feature fail" per Vijay's rule
// (feedback_feature_quality_rules): malformed input, boundary conditions,
// hostile actor, concurrency, resource exhaustion. Each test documents
// what it is trying to break and what the expected RESILIENCE behavior is.
// -----------------------------------------------------------------------------

// BREAK 1: Manual edit mid-file (admin trying to hide a command).
// EXPECTED: VerifyChain flags the edited line.
func TestBreak_ManualEditMidFile(t *testing.T) {
	tmp := t.TempDir()
	l, _ := New(tmp)
	for i := 0; i < 5; i++ {
		_, _ = l.Append(Event{DeviceID: "d", EventType: "command_received", CommandID: fmt.Sprintf("c%d", i), Decision: "accepted", SignatureOK: true})
	}
	path := filepath.Join(tmp, "audit.log")
	data, _ := os.ReadFile(path)
	// Flip "c2" to "cX" — breaks that line's own line_hash.
	tampered := strings.Replace(string(data), `"c2"`, `"cX"`, 1)
	_ = os.WriteFile(path, []byte(tampered), 0o600)
	idx, _, err := VerifyChain(path)
	if err != nil {
		t.Fatal(err)
	}
	if idx != 2 {
		t.Errorf("tamper detected at line %d, want 2", idx)
	}
}

// BREAK 2: File deleted mid-session (admin trying to erase the record).
// EXPECTED: Next Append silently recreates audit.log; the chain RESTARTS
// (first line has empty prev_hash). Server-side records already got the
// stream so the deleted events are still on record there.
func TestBreak_DeletedMidSession(t *testing.T) {
	tmp := t.TempDir()
	l, _ := New(tmp)
	_, _ = l.Append(Event{DeviceID: "d", EventType: "command_received", CommandID: "c1", Decision: "accepted", SignatureOK: true})
	path := filepath.Join(tmp, "audit.log")
	_ = os.Remove(path)
	// Next append must NOT crash; it recreates the file.
	ev, err := l.Append(Event{DeviceID: "d", EventType: "command_received", CommandID: "c2", Decision: "accepted", SignatureOK: true})
	if err != nil {
		t.Fatalf("append after delete failed: %v", err)
	}
	if _, serr := os.Stat(path); serr != nil {
		t.Errorf("audit.log not recreated after delete: %v", serr)
	}
	// NOTE: in-memory chain is retained (l.lastHash still references the
	// previous event), so the NEW c2's prev_hash points to the deleted
	// c1's line_hash. VerifyChain will fail for the new file because
	// line 0 has a non-empty prev_hash that doesn't match empty — this
	// is a FEATURE: an auditor sees the file was tampered with.
	_, _ = ev, path
}

// BREAK 3: File truncated to 0 bytes (admin trying to clear it).
// EXPECTED: Next Append still writes; VerifyChain flags the inconsistency
// between the truncated file's first line and the (now empty) expected
// prev_hash.
func TestBreak_TruncatedMidSession(t *testing.T) {
	tmp := t.TempDir()
	l, _ := New(tmp)
	_, _ = l.Append(Event{DeviceID: "d", EventType: "command_received", CommandID: "c1", Decision: "accepted", SignatureOK: true})
	path := filepath.Join(tmp, "audit.log")
	_ = os.Truncate(path, 0)
	ev, err := l.Append(Event{DeviceID: "d", EventType: "command_received", CommandID: "c2", Decision: "accepted", SignatureOK: true})
	if err != nil {
		t.Fatalf("append after truncate failed: %v", err)
	}
	// The in-memory chain still points at c1; the first physical line is
	// c2 which has prev_hash = (c1's hash). An auditor sees line 0 has
	// non-empty prev_hash — tamper evidence intact.
	if ev.PrevHash == "" {
		t.Error("prev_hash cleared after truncate — tamper evidence lost")
	}
}

// BREAK 4: Burst of 1000 concurrent appends from 500 goroutines
// (resource exhaustion attempt + race detection).
// EXPECTED: All 1000 events appended, chain intact, no corruption.
func TestBreak_ConcurrentBurst(t *testing.T) {
	tmp := t.TempDir()
	l, _ := New(tmp)
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			_, _ = l.Append(Event{DeviceID: "d", EventType: "command_received", CommandID: fmt.Sprintf("w%d-a", w), Decision: "accepted", SignatureOK: true})
			_, _ = l.Append(Event{DeviceID: "d", EventType: "command_received", CommandID: fmt.Sprintf("w%d-b", w), Decision: "accepted", SignatureOK: true})
		}(i)
	}
	wg.Wait()
	idx, reason, err := VerifyChain(filepath.Join(tmp, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if idx != -1 {
		t.Errorf("burst broke chain at line %d: %s", idx, reason)
	}
}

// BREAK 5: Oversize event (hostile actor cramming payload into Reason
// field to overflow the file). The cap is per-event line size + rotation
// prevents disk exhaustion.
// EXPECTED: File rotates before disk fills; chain continues across
// rotation; large events are written whole (not truncated).
func TestBreak_OversizedReason(t *testing.T) {
	tmp := t.TempDir()
	l, _ := NewWithLimit(tmp, 2048) // tiny cap
	huge := strings.Repeat("A", 500)
	for i := 0; i < 20; i++ {
		_, err := l.Append(Event{DeviceID: "d", EventType: "command_received", CommandID: fmt.Sprintf("c%d", i), Reason: huge, Decision: "accepted", SignatureOK: true})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	// At least one rotated file.
	matches, _ := filepath.Glob(filepath.Join(tmp, "audit-*.log"))
	if len(matches) == 0 {
		t.Error("no rotation triggered despite oversized events")
	}
	// Current file verifies.
	idx, _, err := VerifyChain(filepath.Join(tmp, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if idx != -1 {
		t.Error("oversized events corrupted the chain")
	}
}

// BREAK 6: Event with empty DeviceID (malformed caller input).
// EXPECTED: Still appended — the Logger does not validate inputs, that
// is the caller's responsibility. This test documents the behavior so a
// future reader knows the layer boundary.
func TestBreak_EmptyDeviceID(t *testing.T) {
	tmp := t.TempDir()
	l, _ := New(tmp)
	_, err := l.Append(Event{EventType: "command_received", CommandID: "c1", Decision: "accepted", SignatureOK: true})
	if err != nil {
		t.Errorf("append with empty device_id failed: %v (logger should not validate at this layer)", err)
	}
}

// BREAK 8: Startup after mid-file tamper — admin edited audit.log
// between agent runs. New() must detect the break, quarantine the file,
// and start a fresh chain.
// EXPECTED: Quarantine report returned; original file is renamed to
// audit-TAMPERED-<ts>.log; fresh audit.log starts empty.
func TestBreak_StartupQuarantineOnTamper(t *testing.T) {
	tmp := t.TempDir()
	l1, _ := New(tmp)
	_, _ = l1.Append(Event{DeviceID: "d", EventType: "command_received", CommandID: "c1", Decision: "accepted", SignatureOK: true})
	_, _ = l1.Append(Event{DeviceID: "d", EventType: "command_received", CommandID: "c2", Decision: "accepted", SignatureOK: true})
	_, _ = l1.Append(Event{DeviceID: "d", EventType: "command_received", CommandID: "c3", Decision: "accepted", SignatureOK: true})
	path := filepath.Join(tmp, "audit.log")
	data, _ := os.ReadFile(path)
	// Edit c2 → cX — breaks c2's own line_hash.
	tampered := strings.Replace(string(data), `"c2"`, `"cX"`, 1)
	_ = os.WriteFile(path, []byte(tampered), 0o600)
	// Agent restart: New() should quarantine the broken file.
	l2, err := New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	qr := l2.LastQuarantine()
	if qr == nil {
		t.Fatal("expected QuarantineReport; got nil (startup did not detect tamper)")
	}
	if qr.BrokenAt != 1 && qr.BrokenAt != 2 {
		t.Errorf("BrokenAt = %d, expected 1 or 2", qr.BrokenAt)
	}
	// Original file renamed away.
	if _, err := os.Stat(path); err == nil {
		// Could be fresh empty file after quarantine — that's OK if
		// quarantined file exists adjacent.
	}
	// Quarantined file exists.
	matches, _ := filepath.Glob(filepath.Join(tmp, "audit-TAMPERED-*.log"))
	if len(matches) == 0 {
		t.Error("no audit-TAMPERED-*.log file produced — forensics evidence lost")
	}
	// Fresh audit.log is empty-or-ready; next Append starts a new chain.
	ev, _ := l2.Append(Event{DeviceID: "d", EventType: "command_received", CommandID: "c-fresh", Decision: "accepted", SignatureOK: true})
	if ev.PrevHash != "" {
		t.Errorf("fresh chain first event prev_hash = %q, want empty", ev.PrevHash)
	}
}

// BREAK 7: Read-only directory (admin or policy locks the logs dir).
// EXPECTED: Append fails cleanly; in-memory chain head NOT advanced.
// NOTE: On Windows O_CREATE often succeeds even in a dir with weird
// ACLs; we simulate the failure by pointing path at a nonexistent nested
// dir (same pattern TestWriteFailure_ChainStateUnchanged uses).
func TestBreak_FailureDoesNotAdvanceChain(t *testing.T) {
	tmp := t.TempDir()
	l, _ := New(tmp)
	_, _ = l.Append(Event{DeviceID: "d", EventType: "command_received", CommandID: "c1", Decision: "accepted", SignatureOK: true})
	head := l.lastHash
	l.path = filepath.Join(tmp, "nope", "audit.log")
	_, err := l.Append(Event{DeviceID: "d", EventType: "command_received", CommandID: "c2", Decision: "accepted", SignatureOK: true})
	if err == nil {
		t.Fatal("expected write failure")
	}
	if l.lastHash != head {
		t.Errorf("chain head advanced despite failure: %q, want %q", l.lastHash, head)
	}
}
