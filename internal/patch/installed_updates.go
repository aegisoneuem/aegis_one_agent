package patch

import (
	"sort"
	"strings"
)

// normKBDigits strips an optional "KB" prefix and surrounding whitespace and
// returns the bare digit string, or "" when there is no digit run. Used to
// merge KB identifiers from different sources (WUA / QFE / CBS) that format
// them inconsistently ("KB5034123" vs "5034123").
func normKBDigits(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && strings.EqualFold(s[:2], "kb") {
		s = s[2:]
	}
	s = strings.TrimSpace(s)
	// Keep only a leading run of digits.
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return ""
	}
	return s[:end]
}

// unionKBs merges several KB lists into one deduped, sorted set in canonical
// "KB<digits>" form. This is how the agent guarantees nothing is missed: the
// installed set is the UNION of every source (WUA IsInstalled=1, QFE, CBS), so
// a KB any single source misses is still reported if another source has it.
func unionKBs(lists ...[]string) []string {
	set := make(map[string]struct{})
	for _, list := range lists {
		for _, kb := range list {
			if n := normKBDigits(kb); n != "" {
				set["KB"+n] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for kb := range set {
		out = append(out, kb)
	}
	sort.Strings(out)
	return out
}
