package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	"github.com/aegisoneuem/aegis_one_agent/internal/fileops"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// auditQueryDispatcher builds a dispatcher whose <SecureDir>/logs holds the given
// events (oldest-first), plus a buffered outbox to read the ActionStatus from.
func auditQueryDispatcher(t *testing.T, events ...auditlog.Event) (*dispatcher, chan *pb.AgentMessage) {
	t.Helper()
	secure := t.TempDir()
	logDir := filepath.Join(secure, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := auditlog.New(logDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if _, err := l.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	out := make(chan *pb.AgentMessage, 4)
	d := &dispatcher{cfg: config.Config{SecureDir: secure, DeviceID: "dev-1"}, outbox: out}
	return d, out
}

func auditCmd(t *testing.T, spec auditQuerySpec) *pb.Command {
	t.Helper()
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	return &pb.Command{CommandId: "aq-1", Type: "audit_query", Payload: raw}
}

func sampleEvents() []auditlog.Event {
	return []auditlog.Event{
		{TS: 1000, EventType: "command_received", CommandID: "cmd-1", Actor: "vijay@server", Decision: "accepted", Reason: "hostname"},
		{TS: 2000, EventType: "command_completed", CommandID: "cmd-1", Decision: "succeeded", Reason: "ok"},
		{TS: 3000, EventType: "command_received", CommandID: "deploy-1", Actor: "vijay@server", Decision: "accepted", Reason: "install 7zip"},
		{TS: 4000, EventType: "command_rejected", CommandID: "deploy-2", Actor: "mallory@server", Decision: "rejected", Reason: "signature verification failed"},
		{TS: 5000, EventType: "command_completed", CommandID: "deploy-1", Decision: "failed", Reason: "installer failed exit 1603"},
	}
}

func readStatus(t *testing.T, out chan *pb.AgentMessage) *pb.ActionStatus {
	t.Helper()
	st := (<-out).GetActionStatus()
	if st == nil {
		t.Fatal("expected an ActionStatus message")
	}
	return st
}

func decodeMatches(t *testing.T, st *pb.ActionStatus) []auditlog.Event {
	t.Helper()
	var evs []auditlog.Event
	if strings.TrimSpace(st.Detail) == "" {
		return evs
	}
	if err := json.Unmarshal([]byte(st.Detail), &evs); err != nil {
		t.Fatalf("detail is not a JSON event array: %v\n%s", err, st.Detail)
	}
	return evs
}

// --- Integration: the REAL dispatch handler path ---

func TestAuditQuery_Integration_TailReturnsNewestFirst(t *testing.T) {
	d, out := auditQueryDispatcher(t, sampleEvents()...)
	d.handleAuditQueryCmd(context.Background(), nil, auditCmd(t, auditQuerySpec{Version: 1, Query: auditlog.Query{Mode: "tail", Limit: 2}}))
	st := readStatus(t, out)
	if st.Phase != pb.ActionPhase_ACTION_PHASE_SUCCESS {
		t.Fatalf("phase %v reason %q", st.Phase, st.Reason)
	}
	evs := decodeMatches(t, st)
	if len(evs) != 2 || evs[0].TS != 5000 || evs[1].TS != 4000 {
		t.Fatalf("tail-2 newest-first wrong: %+v", evs)
	}
	if st.ActionType != actionAuditQuery {
		t.Fatalf("action_type = %q", st.ActionType)
	}
}

func TestAuditQuery_Integration_Search(t *testing.T) {
	d, out := auditQueryDispatcher(t, sampleEvents()...)
	d.handleAuditQueryCmd(context.Background(), nil, auditCmd(t, auditQuerySpec{Version: 1, Query: auditlog.Query{Mode: "search", Pattern: "7zip"}}))
	st := readStatus(t, out)
	evs := decodeMatches(t, st)
	if len(evs) != 1 || evs[0].CommandID != "deploy-1" || evs[0].TS != 3000 {
		t.Fatalf("search 7zip: %+v", evs)
	}
}

func TestAuditQuery_Integration_IncludesRotated(t *testing.T) {
	enroll := auditlog.Event{TS: 500, EventType: "enrollment", CommandID: "enroll-1", Decision: "succeeded", Reason: "cert issued"}

	// Case A — WITHOUT include_rotated: a rotated event must not appear.
	dA, outA := auditQueryDispatcher(t, sampleEvents()...)
	writeRotated(t, filepath.Join(dA.cfg.SecureDir, "logs", "audit-50.log"), enroll)
	dA.handleAuditQueryCmd(context.Background(), nil, auditCmd(t, auditQuerySpec{Version: 1, Query: auditlog.Query{Pattern: "cert issued"}}))
	if evs := decodeMatches(t, readStatus(t, outA)); len(evs) != 0 {
		t.Fatalf("rotated event must not appear without include_rotated: %+v", evs)
	}

	// Case B — WITH include_rotated: the rotated event is found.
	dB, outB := auditQueryDispatcher(t, sampleEvents()...)
	writeRotated(t, filepath.Join(dB.cfg.SecureDir, "logs", "audit-50.log"), enroll)
	dB.handleAuditQueryCmd(context.Background(), nil, auditCmd(t, auditQuerySpec{Version: 1, IncludeRotated: true, Query: auditlog.Query{Pattern: "cert issued"}}))
	if evs := decodeMatches(t, readStatus(t, outB)); len(evs) != 1 || evs[0].CommandID != "enroll-1" {
		t.Fatalf("rotated event must appear with include_rotated: %+v", evs)
	}
}

// --- Security ---

// A payload carrying extra keys that look like a file path must NOT redirect the
// read: the handler only ever reads the audit dir (no path field exists), so the
// injected path is silently ignored and only audit events come back.
func TestAuditQuery_Security_NoArbitraryPathRead(t *testing.T) {
	d, out := auditQueryDispatcher(t, sampleEvents()...)
	// Hand-craft a payload with a bogus "source"/"path" the struct does not define.
	raw := []byte(`{"version":1,"mode":"tail","limit":10,"source":"C:\\Windows\\System32\\config\\SAM","path":"C:\\secret.txt"}`)
	d.handleAuditQueryCmd(context.Background(), nil, &pb.Command{CommandId: "aq-sec", Type: "audit_query", Payload: raw})
	st := readStatus(t, out)
	if st.Phase != pb.ActionPhase_ACTION_PHASE_SUCCESS {
		t.Fatalf("phase %v", st.Phase)
	}
	evs := decodeMatches(t, st)
	if len(evs) != 5 {
		t.Fatalf("expected the 5 audit events (injected path ignored), got %d", len(evs))
	}
	// Every returned record must be a real audit event, never foreign file bytes.
	for _, e := range evs {
		if e.EventType == "" || e.Decision == "" {
			t.Fatalf("returned a non-audit record: %+v", e)
		}
	}
}

// A huge limit must be clamped, never honored unbounded.
func TestAuditQuery_Security_LimitClamped(t *testing.T) {
	d, out := auditQueryDispatcher(t, sampleEvents()...)
	d.handleAuditQueryCmd(context.Background(), nil, auditCmd(t, auditQuerySpec{Version: 1, Query: auditlog.Query{Limit: 10_000_000}}))
	st := readStatus(t, out)
	evs := decodeMatches(t, st)
	if len(evs) > auditlog.MaxQueryLimit {
		t.Fatalf("limit not clamped: returned %d > %d", len(evs), auditlog.MaxQueryLimit)
	}
	if len(evs) != 5 { // all 5 present (fewer than the ceiling)
		t.Fatalf("expected 5, got %d", len(evs))
	}
}

// --- Break / negative ---

func TestAuditQuery_Break_MalformedPayload(t *testing.T) {
	cases := [][]byte{
		[]byte(`not json`),
		[]byte(`{"version":2,"mode":"tail"}`),       // wrong version
		[]byte(`{"version":1,"mode":"search"}`),      // search without pattern
		[]byte(`{"version":1,"mode":"bogus"}`),       // unknown mode
		[]byte(`{"version":1,"since_ms":9,"until_ms":1,"mode":"since"}`), // inverted range
	}
	for i, raw := range cases {
		d, out := auditQueryDispatcher(t, sampleEvents()...)
		d.handleAuditQueryCmd(context.Background(), nil, &pb.Command{CommandId: "aq-bad", Type: "audit_query", Payload: raw})
		st := readStatus(t, out)
		if st.Phase != pb.ActionPhase_ACTION_PHASE_FAILED {
			t.Fatalf("case %d: expected FAILED, got %v (%q)", i, st.Phase, st.Reason)
		}
	}
}

func TestAuditQuery_Break_EmptyLogSucceedsWithZero(t *testing.T) {
	d, out := auditQueryDispatcher(t) // no events written
	d.handleAuditQueryCmd(context.Background(), nil, auditCmd(t, auditQuerySpec{Version: 1, Query: auditlog.Query{Mode: "tail"}}))
	st := readStatus(t, out)
	if st.Phase != pb.ActionPhase_ACTION_PHASE_SUCCESS {
		t.Fatalf("empty log should still succeed, got %v", st.Phase)
	}
	if evs := decodeMatches(t, st); len(evs) != 0 {
		t.Fatalf("expected 0 events, got %d", len(evs))
	}
}

// A large match set must be trimmed to the wire cap, keeping the newest and
// reporting the omission — never emit an oversized Detail.
func TestAuditQuery_Break_ResultTrimmedToSizeCap(t *testing.T) {
	bigReason := strings.Repeat("x", 4000)
	events := make([]auditlog.Event, 0, 600)
	for i := 0; i < 600; i++ {
		events = append(events, auditlog.Event{TS: int64(i + 1), EventType: "command_received", CommandID: fmt.Sprintf("c%d", i), Decision: "accepted", Reason: bigReason})
	}
	d, out := auditQueryDispatcher(t, events...)
	d.handleAuditQueryCmd(context.Background(), nil, auditCmd(t, auditQuerySpec{Version: 1, Query: auditlog.Query{Limit: 1000}}))
	st := readStatus(t, out)
	if int64(len(st.Detail)) > fileops.MaxManifestBytes {
		t.Fatalf("detail %d exceeds cap %d", len(st.Detail), fileops.MaxManifestBytes)
	}
	if !strings.Contains(st.Reason, "omitted to fit the size cap") {
		t.Fatalf("expected trim note in reason: %q", st.Reason)
	}
	evs := decodeMatches(t, st)
	if len(evs) == 0 || len(evs) >= 600 {
		t.Fatalf("expected a trimmed subset, got %d", len(evs))
	}
	// The newest event (highest TS) must be kept.
	if evs[0].TS != 600 {
		t.Fatalf("trim must keep the newest event, got first TS %d", evs[0].TS)
	}
}

func writeRotated(t *testing.T, path string, evs ...auditlog.Event) {
	t.Helper()
	var buf strings.Builder
	for _, e := range evs {
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(raw)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}
