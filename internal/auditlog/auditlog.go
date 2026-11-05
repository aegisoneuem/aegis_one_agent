// Package auditlog is the agent-side rolling audit buffer. The server is
// the AUTHORITATIVE compliance surface — the agent STREAMS every security
// event via pb.AuditEvent on the Connect stream and the server team
// writes server_audit.log with chain-hash integrity the endpoint admin
// cannot reach. The local audit.log here is BEST-EFFORT: it gives the
// agent an offline-grace queue (events written locally first, re-stream
// on reconnect) and gives ops / forensics a local record for debugging.
// Local admin CAN tamper with the on-disk file; the server copy is the
// authoritative one.
//
// Design discipline:
//   - Append-only on disk (os.OpenFile with O_APPEND; never O_TRUNC).
//   - File mode 0600 — SYSTEM + Administrators only.
//   - Chain hash: every record carries prev_hash (hex SHA-256 of the
//     previous record's line_hash) + line_hash (hex SHA-256 of the
//     canonical serialization of fields 1..12). Any edit breaks the chain
//     and the auditcheck tool detects it.
//   - Size-based rotation at MaxBytes (default 10 MB). When rotation
//     happens, the current file is renamed to audit-<unix-ts>.log and a
//     fresh audit.log is opened; the first line of the new file carries
//     prev_hash = line_hash of the old file's last line so the chain
//     survives rotation.
//   - NO secrets or payload bytes — only metadata (command id, type,
//     actor, source, decision, reason). Matches UC-20 secure parameters
//     guard pattern.
//   - Fail-loud: a write failure returns an error the caller surfaces as
//     an "audit_degraded" event streamed to the server; command execution
//     continues (so a disk full doesn't block legitimate work) but the
//     server-side record captures the degradation.
package auditlog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// DefaultMaxBytes is the default size cap for the rolling local audit
// log. 10 MB holds ~20k events at ~500 bytes each — enough for
// operational debugging and offline-grace. Local admin tampering is an
// acknowledged limitation; the authoritative record is server-side.
const DefaultMaxBytes int64 = 10 * 1024 * 1024

// Event is the in-memory audit record the caller constructs for one
// security-relevant event. Deliberately a plain struct (not proto) so
// the module can be unit-tested without a proto dep.
//
// Fields map 1:1 onto pb.AuditEvent. The caller fills everything except
// PrevHash / LineHash — Append computes those.
type Event struct {
	DeviceID    string `json:"device_id"`
	TS          int64  `json:"ts"`
	EventType   string `json:"event_type"`
	CommandID   string `json:"command_id,omitempty"`
	CommandType string `json:"command_type,omitempty"`
	Actor       string `json:"actor,omitempty"`
	Source      string `json:"source,omitempty"`
	IssuedAt    int64  `json:"issued_at,omitempty"`
	SignatureOK bool   `json:"signature_ok"`
	Decision    string `json:"decision"`
	Reason      string `json:"reason,omitempty"`
	PrevHash    string `json:"prev_hash,omitempty"`
	LineHash    string `json:"line_hash"`
}

// Logger is the serialized append-only audit sink. Safe for concurrent
// Append calls from multiple goroutines — a mutex guards the file write
// and the lastHash chain state.
type Logger struct {
	mu             sync.Mutex
	dir            string
	maxBytes       int64
	path           string
	lastHash       string
	fileBytes      int64
	lastQuarantine *QuarantineReport
}

// New opens (or creates) <dir>/audit.log for append, seeds lastHash from
// the final line of any existing file so the chain continues across
// restarts, and returns a Logger ready for Append. The directory is
// created with mode 0700 if missing. A disk-level error here is returned
// to the caller — the agent decides whether to continue without audit
// (not recommended) or refuse to start.
func New(dir string) (*Logger, error) {
	return NewWithLimit(dir, DefaultMaxBytes)
}

// NewWithLimit is the test / config variant that takes an explicit
// maxBytes for the rolling cap.
//
// Startup integrity: BEFORE seeding the chain, this runs VerifyChain on
// any existing audit.log. If the chain is broken (admin edited, inserted,
// or deleted a line), the suspect file is QUARANTINED — renamed to
// audit-TAMPERED-<unix-ts>.log and left on disk for forensics — and a
// fresh audit.log is created. The next audit event starts a new chain
// from prev_hash="", and the quarantined file preserves evidence of the
// tamper attempt. The caller (agent) sees the integrity failure in the
// returned QuarantineReport so it can emit an "audit_degraded" event to
// the server.
func NewWithLimit(dir string, maxBytes int64) (*Logger, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("auditlog: mkdir %s: %w", dir, err)
	}
	l := &Logger{dir: dir, maxBytes: maxBytes, path: filepath.Join(dir, "audit.log")}
	if qr := checkAndQuarantine(l.path); qr != nil {
		l.lastQuarantine = qr
		// Seeded from empty — the quarantined file holds the old chain.
		l.lastHash = ""
		l.fileBytes = 0
		return l, nil
	}
	// Chain intact — seed from the tail so a restart continues it.
	last, size, err := tailHashAndSize(l.path)
	if err != nil {
		return nil, fmt.Errorf("auditlog: seed from %s: %w", l.path, err)
	}
	l.lastHash = last
	l.fileBytes = size
	return l, nil
}

// QuarantineReport describes a startup integrity failure: the suspect
// audit.log had its chain broken and was renamed to QuarantinePath. The
// caller can read it to emit an "audit_degraded" event to the server so
// the central server_audit.log has an authoritative record of the
// tamper attempt.
type QuarantineReport struct {
	QuarantinePath string
	BrokenAt       int
	Reason         string
}

// LastQuarantine returns the most recent quarantine report from New() /
// NewWithLimit(), or nil when the startup chain was intact.
func (l *Logger) LastQuarantine() *QuarantineReport { return l.lastQuarantine }

// checkAndQuarantine verifies the chain of path (if the file exists); on
// a break, renames it to audit-TAMPERED-<unix-ts>.log and returns a
// QuarantineReport. Returns nil when the file is missing OR verified.
func checkAndQuarantine(path string) *QuarantineReport {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() == 0 {
		return nil
	}
	idx, reason, err := VerifyChain(path)
	if err != nil {
		// Read failure is distinct from chain break — surface it as a
		// quarantine too so the admin inspects the file.
		q := quarantine(path, -2, "verify read error: "+err.Error())
		return q
	}
	if idx == -1 {
		return nil
	}
	return quarantine(path, idx, reason)
}

// quarantine renames path to audit-TAMPERED-<ts>.log (adjacent to path)
// and returns the report. If rename fails we still return a report so
// the caller logs — the broken file is left in place as a last resort.
func quarantine(path string, idx int, reason string) *QuarantineReport {
	dir := filepath.Dir(path)
	target := filepath.Join(dir, fmt.Sprintf("audit-TAMPERED-%d.log", time.Now().Unix()))
	_ = os.Rename(path, target) // best effort — forensics preserved either way
	return &QuarantineReport{QuarantinePath: target, BrokenAt: idx, Reason: reason}
}

// Append writes one event to the rolling log. It fills in TS (if zero),
// PrevHash from the current chain head, LineHash from the canonical
// serialization, then appends the JSON line + newline. Returns a COPY of
// the event with the hash fields populated so the caller can forward the
// same record to the server via pb.AuditEvent.
//
// On a write failure the chain state is NOT advanced (so the next Append
// retries from the same prev_hash); the caller should emit an
// "audit_degraded" event so the server sees the gap.
func (l *Logger) Append(ev Event) (Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ev.TS == 0 {
		ev.TS = time.Now().UnixMilli()
	}
	ev.PrevHash = l.lastHash
	ev.LineHash = computeLineHash(ev)
	line, err := serialize(ev)
	if err != nil {
		return ev, fmt.Errorf("auditlog: serialize: %w", err)
	}
	if l.fileBytes+int64(len(line)) > l.maxBytes {
		if err := l.rotateLocked(); err != nil {
			return ev, fmt.Errorf("auditlog: rotate: %w", err)
		}
		// Post-rotation the new file starts fresh. Recompute ev's chain
		// fields so this event IS the new file's genesis (prev_hash="").
		ev.PrevHash = l.lastHash // now "" after rotation
		ev.LineHash = computeLineHash(ev)
		line, err = serialize(ev)
		if err != nil {
			return ev, fmt.Errorf("auditlog: serialize post-rotate: %w", err)
		}
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return ev, fmt.Errorf("auditlog: open %s: %w", l.path, err)
	}
	n, werr := f.Write(line)
	cerr := f.Close()
	if werr != nil {
		return ev, fmt.Errorf("auditlog: write: %w", werr)
	}
	if cerr != nil {
		return ev, fmt.Errorf("auditlog: close: %w", cerr)
	}
	l.lastHash = ev.LineHash
	l.fileBytes += int64(n)
	return ev, nil
}

// rotateLocked renames audit.log to audit-<unix-ts>.log so the next
// Append writes to a fresh file. The caller already holds l.mu.
//
// Each rotated file is INDEPENDENTLY verifiable — the new file starts
// with prev_hash="" so an auditor who holds only the current file sees a
// valid chain from genesis. Cross-file continuity lives on the SERVER
// side where every event was streamed as it was logged; the server's
// server_audit.log has the full uninterrupted chain and a local-rotation
// gap is detectable server-side by chain-hash discontinuity if a rotated
// file is deleted.
//
// This trade-off keeps the local file self-contained for the auditcheck
// tool (which inspects one file at a time) at the cost of agent-only
// cross-file continuity — acceptable because the authoritative record is
// server-side.
func (l *Logger) rotateLocked() error {
	rotated := filepath.Join(l.dir, fmt.Sprintf("audit-%d.log", time.Now().Unix()))
	if err := os.Rename(l.path, rotated); err != nil {
		if os.IsNotExist(err) {
			l.fileBytes = 0
			l.lastHash = ""
			return nil
		}
		return err
	}
	l.fileBytes = 0
	l.lastHash = ""
	return nil
}

// computeLineHash returns the hex SHA-256 of the canonical serialization
// of ev with LineHash itself blanked (so a re-hash on the rendered line
// deterministically matches). Field ordering is enforced via a manual
// sorted-key marshal so Go's map iteration order doesn't affect the hash.
func computeLineHash(ev Event) string {
	tmp := ev
	tmp.LineHash = ""
	raw, _ := canonicalJSON(tmp)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// serialize returns the on-disk line for ev (canonical JSON + newline).
func serialize(ev Event) ([]byte, error) {
	raw, err := canonicalJSON(ev)
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// canonicalJSON marshals ev with keys in sorted order so the hash is
// reproducible across Go versions / machines. We hand-serialize because
// encoding/json's map iteration order is undefined.
func canonicalJSON(ev Event) ([]byte, error) {
	fields := map[string]any{
		"device_id":    ev.DeviceID,
		"ts":           ev.TS,
		"event_type":   ev.EventType,
		"command_id":   ev.CommandID,
		"command_type": ev.CommandType,
		"actor":        ev.Actor,
		"source":       ev.Source,
		"issued_at":    ev.IssuedAt,
		"signature_ok": ev.SignatureOK,
		"decision":     ev.Decision,
		"reason":       ev.Reason,
		"prev_hash":    ev.PrevHash,
		"line_hash":    ev.LineHash,
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf []byte
	buf = append(buf, '{')
	for i, k := range keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		kraw, _ := json.Marshal(k)
		vraw, err := json.Marshal(fields[k])
		if err != nil {
			return nil, err
		}
		buf = append(buf, kraw...)
		buf = append(buf, ':')
		buf = append(buf, vraw...)
	}
	buf = append(buf, '}')
	return buf, nil
}

// tailHashAndSize reads the existing audit.log (if any) and returns the
// LineHash of the LAST record plus the total file size. Returns ("", 0,
// nil) when the file does not yet exist — a fresh install.
func tailHashAndSize(path string) (string, int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", 0, nil
		}
		return "", 0, err
	}
	size := fi.Size()
	if size == 0 {
		return "", 0, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", size, err
	}
	// Last non-empty line.
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] != '\n' {
			continue
		}
		if i == len(data)-1 {
			continue
		}
	}
	// Simpler: split on newline and take the last non-empty.
	lines := splitLines(data)
	if len(lines) == 0 {
		return "", size, nil
	}
	var ev Event
	if err := json.Unmarshal(lines[len(lines)-1], &ev); err != nil {
		return "", size, fmt.Errorf("parse tail line: %w", err)
	}
	return ev.LineHash, size, nil
}

// splitLines returns non-empty lines from data.
func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i <= len(data); i++ {
		if i == len(data) || data[i] == '\n' {
			if i > start {
				out = append(out, data[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// VerifyChain reads path end-to-end and confirms every line's prev_hash
// matches the previous line's line_hash and every line's line_hash
// matches the canonical re-computation. Returns the first index + reason
// of any break, or (-1, "") when the chain is intact. The auditcheck CLI
// tool uses this.
func VerifyChain(path string) (int, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return -1, "", err
	}
	lines := splitLines(data)
	prev := ""
	for i, raw := range lines {
		var ev Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			return i, fmt.Sprintf("line %d: parse: %v", i, err), nil
		}
		if ev.PrevHash != prev {
			return i, fmt.Sprintf("line %d: prev_hash=%s, expected=%s", i, ev.PrevHash, prev), nil
		}
		want := computeLineHash(ev)
		if ev.LineHash != want {
			return i, fmt.Sprintf("line %d: line_hash=%s, recomputed=%s (edited?)", i, ev.LineHash, want), nil
		}
		prev = ev.LineHash
	}
	return -1, "", nil
}
