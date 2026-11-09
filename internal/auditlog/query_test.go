package auditlog

import (
	"os"
	"path/filepath"
	"testing"
)

func ev(ts int64, typ, cmdID, actor, decision, reason string) Event {
	return Event{TS: ts, EventType: typ, CommandID: cmdID, Actor: actor, Decision: decision, Reason: reason}
}

// sample returns a deterministic oldest-first event set.
func sample() []Event {
	return []Event{
		ev(1000, "command_received", "cmd-1", "vijay@server", "accepted", "hostname"),
		ev(2000, "command_completed", "cmd-1", "", "succeeded", "ok"),
		ev(3000, "command_received", "deploy-1", "vijay@server", "accepted", "install 7zip"),
		ev(4000, "command_rejected", "deploy-2", "mallory@server", "rejected", "signature verification failed"),
		ev(5000, "command_completed", "deploy-1", "", "failed", "installer failed exit 1603"),
	}
}

func ids(evs []Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.CommandID
	}
	return out
}

func TestFilter_TailNewestFirstAndLimit(t *testing.T) {
	got := Filter(sample(), Query{Mode: "tail", Limit: 2})
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	// Newest-first: the last two events (ts 5000 then 4000).
	if got[0].TS != 5000 || got[1].TS != 4000 {
		t.Fatalf("tail order wrong: %d then %d", got[0].TS, got[1].TS)
	}
}

func TestFilter_DefaultLimitAndClamp(t *testing.T) {
	// default limit returns all 5 (fewer than DefaultQueryLimit), newest-first.
	all := Filter(sample(), Query{})
	if len(all) != 5 || all[0].TS != 5000 {
		t.Fatalf("default: got %d events, first ts %d", len(all), all[0].TS)
	}
	// clamp: a limit above the ceiling is capped, never rejected.
	if q := (Query{Limit: MaxQueryLimit + 500}); q.limit() != MaxQueryLimit {
		t.Fatalf("limit clamp: got %d", q.limit())
	}
}

func TestFilter_SearchCaseInsensitiveAcrossFields(t *testing.T) {
	// matches reason "install 7zip" and actor, case-insensitive
	got := Filter(sample(), Query{Mode: "search", Pattern: "7ZIP"})
	if len(got) != 1 || got[0].CommandID != "deploy-1" {
		t.Fatalf("search 7zip: %v", ids(got))
	}
	// actor substring hits two received events
	got = Filter(sample(), Query{Pattern: "mallory"})
	if len(got) != 1 || got[0].CommandID != "deploy-2" {
		t.Fatalf("search mallory: %v", ids(got))
	}
}

func TestFilter_TimeRange(t *testing.T) {
	got := Filter(sample(), Query{SinceMS: 2500, UntilMS: 4000})
	// events ts 3000, 4000 -> newest-first 4000,3000
	if len(got) != 2 || got[0].TS != 4000 || got[1].TS != 3000 {
		t.Fatalf("time range: %v", got)
	}
}

func TestFilter_EventTypeAndDecision(t *testing.T) {
	got := Filter(sample(), Query{EventTypes: []string{"COMMAND_REJECTED"}})
	if len(got) != 1 || got[0].Decision != "rejected" {
		t.Fatalf("type filter: %v", ids(got))
	}
	got = Filter(sample(), Query{Decisions: []string{"failed"}})
	if len(got) != 1 || got[0].CommandID != "deploy-1" || got[0].TS != 5000 {
		t.Fatalf("decision filter: %v", ids(got))
	}
}

func TestFilter_NoMatch(t *testing.T) {
	if got := Filter(sample(), Query{Pattern: "no-such-string"}); len(got) != 0 {
		t.Fatalf("expected empty, got %d", len(got))
	}
	if got := Filter(nil, Query{}); len(got) != 0 {
		t.Fatalf("empty input must yield empty, got %d", len(got))
	}
}

func TestQueryValidate(t *testing.T) {
	bad := []Query{
		{Mode: "search"},               // no pattern
		{Mode: "since"},                // no since_ms
		{Mode: "bogus"},                // unknown mode
		{Limit: -1},                    // negative limit
		{SinceMS: 5000, UntilMS: 1000}, // since after until
		{SinceMS: -1},                  // negative bound
	}
	for i, q := range bad {
		if err := q.Validate(); err == nil {
			t.Fatalf("case %d: expected validation error for %+v", i, q)
		}
	}
	good := []Query{{}, {Mode: "tail"}, {Mode: "search", Pattern: "x"}, {Mode: "since", SinceMS: 1}, {Limit: 50}}
	for i, q := range good {
		if err := q.Validate(); err != nil {
			t.Fatalf("case %d: unexpected error: %v", i, err)
		}
	}
}

func TestReadAll_RotationOrderAndMissing(t *testing.T) {
	dir := t.TempDir()
	// Rotated files out of lexical order to prove numeric sort (audit-9 < audit-100).
	writeJSONL(t, filepath.Join(dir, "audit-100.log"), ev(3000, "command_received", "c3", "", "accepted", ""))
	writeJSONL(t, filepath.Join(dir, "audit-9.log"), ev(1000, "command_received", "c1", "", "accepted", ""))
	writeJSONL(t, filepath.Join(dir, "audit.log"), ev(9000, "command_received", "live", "", "accepted", ""))

	// includeRotated=false → only the live file.
	live, err := ReadAll(dir, false)
	if err != nil || len(live) != 1 || live[0].CommandID != "live" {
		t.Fatalf("live-only read: %v err=%v", ids(live), err)
	}
	// includeRotated=true → audit-9 (ts1000), audit-100 (ts3000), live (ts9000), oldest-first.
	all, err := ReadAll(dir, true)
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	gotIDs := ids(all)
	want := []string{"c1", "c3", "live"}
	for i := range want {
		if i >= len(gotIDs) || gotIDs[i] != want[i] {
			t.Fatalf("rotation order wrong: got %v want %v", gotIDs, want)
		}
	}
}

func TestReadAll_MissingDirIsEmptyNotFatal(t *testing.T) {
	got, err := ReadAll(filepath.Join(t.TempDir(), "nope"), true)
	if err != nil || len(got) != 0 {
		t.Fatalf("missing dir should be empty + no error, got %d err=%v", len(got), err)
	}
}

func writeJSONL(t *testing.T, path string, evs ...Event) {
	t.Helper()
	l := &Logger{dir: filepath.Dir(path), maxBytes: DefaultMaxBytes, path: path}
	for _, e := range evs {
		if _, err := l.Append(e); err != nil {
			t.Fatalf("append fixture: %v", err)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fixture not written: %v", err)
	}
}
