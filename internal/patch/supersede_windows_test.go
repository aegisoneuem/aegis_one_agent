//go:build windows

package patch

import "testing"

func TestMarkSuperseded(t *testing.T) {
	t.Run("flags predecessor when superseder is in the set", func(t *testing.T) {
		updates := []Update{
			{UpdateID: "old-1", SizeMB: 500},
			{UpdateID: "new-1", SizeMB: 700, SupersededUpdateIDs: []string{"old-1"}},
		}
		tagged := markSuperseded(updates)
		if tagged != 1 {
			t.Fatalf("tagged = %d, want 1", tagged)
		}
		if !updates[0].Superseded {
			t.Fatalf("old-1 should be flagged superseded")
		}
		if updates[1].Superseded {
			t.Fatalf("new-1 (the superseder) must NOT be flagged")
		}
		if len(updates) != 2 {
			t.Fatalf("markSuperseded must not drop entries, got %d", len(updates))
		}
	})

	t.Run("case-insensitive GUID match", func(t *testing.T) {
		updates := []Update{
			{UpdateID: "ABC-DEF"},
			{UpdateID: "new", SupersededUpdateIDs: []string{"abc-def"}},
		}
		if markSuperseded(updates) != 1 || !updates[0].Superseded {
			t.Fatalf("case-folded match failed: %+v", updates)
		}
	})

	t.Run("keeps predecessor untagged when superseder absent (installed)", func(t *testing.T) {
		// 'new-1' (the superseder) is not in the result set — e.g. already
		// installed — so its predecessor must NOT be flagged on its account.
		updates := []Update{
			{UpdateID: "old-1", SizeMB: 500},
			{UpdateID: "unrelated", SizeMB: 100},
		}
		if markSuperseded(updates) != 0 || updates[0].Superseded || updates[1].Superseded {
			t.Fatalf("nothing should be flagged: %+v", updates)
		}
	})

	t.Run("no supersedence is a no-op", func(t *testing.T) {
		updates := []Update{{UpdateID: "a"}, {UpdateID: "b"}}
		if markSuperseded(updates) != 0 {
			t.Fatalf("no-op expected")
		}
	})

	t.Run("chain: flags all but the newest", func(t *testing.T) {
		// v3 supersedes v2, v2 supersedes v1. Both v1 and v2 are superseded by
		// something in the set, so only v3 stays actionable.
		updates := []Update{
			{UpdateID: "v1", SizeMB: 300},
			{UpdateID: "v2", SizeMB: 400, SupersededUpdateIDs: []string{"v1"}},
			{UpdateID: "v3", SizeMB: 500, SupersededUpdateIDs: []string{"v2"}},
		}
		tagged := markSuperseded(updates)
		if tagged != 2 || !updates[0].Superseded || !updates[1].Superseded || updates[2].Superseded {
			t.Fatalf("chain tag failed: tagged=%d %+v", tagged, updates)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		if markSuperseded(nil) != 0 {
			t.Fatalf("empty: want 0")
		}
	})
}
