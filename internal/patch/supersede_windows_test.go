//go:build windows

package patch

import "testing"

func TestDropSuperseded(t *testing.T) {
	t.Run("drops predecessor when superseder is in the set", func(t *testing.T) {
		updates := []Update{
			{UpdateID: "old-1", SizeMB: 500},
			{UpdateID: "new-1", SizeMB: 700, SupersededUpdateIDs: []string{"old-1"}},
		}
		out, dropped := dropSuperseded(updates)
		if dropped != 1 {
			t.Fatalf("dropped = %d, want 1", dropped)
		}
		if len(out) != 1 || out[0].UpdateID != "new-1" {
			t.Fatalf("out = %+v, want only new-1", out)
		}
	})

	t.Run("case-insensitive GUID match", func(t *testing.T) {
		updates := []Update{
			{UpdateID: "ABC-DEF"},
			{UpdateID: "new", SupersededUpdateIDs: []string{"abc-def"}},
		}
		out, dropped := dropSuperseded(updates)
		if dropped != 1 || len(out) != 1 || out[0].UpdateID != "new" {
			t.Fatalf("case-folded match failed: out=%+v dropped=%d", out, dropped)
		}
	})

	t.Run("keeps predecessor when superseder absent (installed)", func(t *testing.T) {
		// 'new-1' (the superseder) is not in the result set — e.g. already
		// installed — so its predecessor must NOT be dropped on its account.
		updates := []Update{
			{UpdateID: "old-1", SizeMB: 500},
			{UpdateID: "unrelated", SizeMB: 100},
		}
		out, dropped := dropSuperseded(updates)
		if dropped != 0 || len(out) != 2 {
			t.Fatalf("should keep both: out=%+v dropped=%d", out, dropped)
		}
	})

	t.Run("no supersedence is a no-op", func(t *testing.T) {
		updates := []Update{{UpdateID: "a"}, {UpdateID: "b"}}
		out, dropped := dropSuperseded(updates)
		if dropped != 0 || len(out) != 2 {
			t.Fatalf("no-op expected: out=%+v dropped=%d", out, dropped)
		}
	})

	t.Run("chain: keeps only the newest", func(t *testing.T) {
		// v3 supersedes v2, v2 supersedes v1. Both v1 and v2 are superseded by
		// something in the set, so only v3 survives.
		updates := []Update{
			{UpdateID: "v1", SizeMB: 300},
			{UpdateID: "v2", SizeMB: 400, SupersededUpdateIDs: []string{"v1"}},
			{UpdateID: "v3", SizeMB: 500, SupersededUpdateIDs: []string{"v2"}},
		}
		out, dropped := dropSuperseded(updates)
		if dropped != 2 || len(out) != 1 || out[0].UpdateID != "v3" {
			t.Fatalf("chain collapse failed: out=%+v dropped=%d", out, dropped)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		out, dropped := dropSuperseded(nil)
		if dropped != 0 || len(out) != 0 {
			t.Fatalf("empty: out=%+v dropped=%d", out, dropped)
		}
	})
}
