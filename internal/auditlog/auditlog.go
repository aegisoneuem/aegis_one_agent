// Package auditlog is the agent-side rolling local audit trace. The
// SERVER is the sole authoritative compliance surface — every
// pb.AuditEvent is streamed on the Connect stream and the server team
// writes server_audit.log with the central integrity controls. The local
// file here is BEST-EFFORT, read by field engineers via auditcheck.exe
// for on-endpoint L1 forensics (no server portal access) and to capture
// pre-enrollment events the server never saw.
//
// Design discipline:
//   - Append-only JSONL on disk (O_APPEND; never O_TRUNC).
//   - File mode 0600 — SYSTEM + Administrators only.
//   - Size-based rotation at MaxBytes (default 10 MB). The previous file
//     is renamed audit-<unix-ts>.log; a fresh audit.log is opened.
//   - NO chain hash. Local admin can edit the file and we do not
//     pretend otherwise — detecting a tampered local file does not
//     protect the server record (which is the authoritative one), so
//     we don't do the theatre.
//   - NO secrets or payload bytes — only metadata (command id, type,
//     actor, source, decision, reason, details). Matches UC-20 secure
//     parameters guard pattern.
//   - Fail-loud: a write failure returns an error the caller surfaces as
//     an "audit_degraded" event streamed to the server; command
//     execution continues (so a disk full doesn't block legitimate work)
//     but the server-side record captures the degradation.
package auditlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultMaxBytes is the default size cap for the rolling local audit
// log. 10 MB holds ~20k events at ~500 bytes each — enough for
// on-endpoint L1 forensics between rotations.
const DefaultMaxBytes int64 = 10 * 1024 * 1024

// Event is the in-memory audit record the caller constructs for one
// security-relevant event. Deliberately a plain struct (not proto) so
// the module can be unit-tested without a proto dep. Fields map 1:1
// onto pb.AuditEvent.
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
	// Details: short per-command-type payload summary so an auditor can
	// tell WHAT was done, not just that "a command was received". Format
	// and per-type keys documented on pb.AuditEvent.details.
	Details string `json:"details,omitempty"`
}

// Logger is the serialized append-only audit sink. Safe for concurrent
// Append calls — a mutex guards the file write + rotation state.
type Logger struct {
	mu        sync.Mutex
	dir       string
	maxBytes  int64
	path      string
	fileBytes int64
}

// New opens (or creates) <dir>/audit.log for append. The directory is
// created with mode 0700 if missing.
func New(dir string) (*Logger, error) {
	return NewWithLimit(dir, DefaultMaxBytes)
}

// NewWithLimit is the test / config variant that takes an explicit
// maxBytes for the rolling cap.
func NewWithLimit(dir string, maxBytes int64) (*Logger, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("auditlog: mkdir %s: %w", dir, err)
	}
	l := &Logger{dir: dir, maxBytes: maxBytes, path: filepath.Join(dir, "audit.log")}
	if fi, err := os.Stat(l.path); err == nil {
		l.fileBytes = fi.Size()
	}
	return l, nil
}

// Append writes one event to the rolling log. It fills in TS (if zero),
// serializes to JSON + newline, and appends. Returns a COPY of the
// event (unchanged apart from TS) so the caller can forward the same
// record to the server via pb.AuditEvent.
//
// On a write failure the caller should emit an "audit_degraded" event
// so the server sees the gap.
func (l *Logger) Append(ev Event) (Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ev.TS == 0 {
		ev.TS = time.Now().UnixMilli()
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return ev, fmt.Errorf("auditlog: marshal: %w", err)
	}
	line := append(raw, '\n')
	if l.fileBytes+int64(len(line)) > l.maxBytes {
		if err := l.rotateLocked(); err != nil {
			return ev, fmt.Errorf("auditlog: rotate: %w", err)
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
	l.fileBytes += int64(n)
	return ev, nil
}

// rotateLocked renames audit.log to audit-<unix-ts>.log so the next
// Append writes to a fresh file. The caller already holds l.mu.
func (l *Logger) rotateLocked() error {
	rotated := filepath.Join(l.dir, fmt.Sprintf("audit-%d.log", time.Now().Unix()))
	if err := os.Rename(l.path, rotated); err != nil {
		if os.IsNotExist(err) {
			l.fileBytes = 0
			return nil
		}
		return err
	}
	l.fileBytes = 0
	return nil
}

// Read returns all events currently in audit.log, oldest first. Used by
// the auditcheck pretty-print tool. Caller is responsible for choosing
// whether to read rotated files separately.
func Read(path string) ([]Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Event
	start := 0
	for i := 0; i <= len(data); i++ {
		if i == len(data) || data[i] == '\n' {
			if i > start {
				var ev Event
				if err := json.Unmarshal(data[start:i], &ev); err != nil {
					return out, fmt.Errorf("line %d: %w", len(out), err)
				}
				out = append(out, ev)
			}
			start = i + 1
		}
	}
	return out, nil
}
