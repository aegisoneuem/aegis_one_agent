package auditlog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Query filters the local audit trace for a remote audit-query command. All
// fields are optional; an empty Query returns the most recent DefaultQueryLimit
// events. The filter is read-only and operates purely on already-parsed Events,
// so it is trivially unit-testable and carries no filesystem or network surface.
type Query struct {
	// Mode is advisory ("tail" | "search" | "since") — it documents intent and
	// drives required-field validation, but the actual selection is the AND of
	// whichever filters below are set. Empty defaults to "tail".
	Mode string `json:"mode,omitempty"`
	// Limit caps the number of events returned (most recent first). <=0 uses
	// DefaultQueryLimit; anything above MaxQueryLimit is clamped to MaxQueryLimit
	// so a remote caller can never pull an unbounded slice.
	Limit int `json:"limit,omitempty"`
	// Pattern is a case-insensitive substring matched across the human-meaningful
	// fields (event_type, command id/type, actor, source, decision, reason,
	// details). Empty matches everything.
	Pattern string `json:"pattern,omitempty"`
	// SinceMS / UntilMS bound the event timestamp (UnixMilli), inclusive. 0 = no
	// bound on that end.
	SinceMS int64 `json:"since_ms,omitempty"`
	UntilMS int64 `json:"until_ms,omitempty"`
	// EventTypes / Decisions restrict to the given values (case-insensitive,
	// any-of). Empty = no restriction.
	EventTypes []string `json:"event_types,omitempty"`
	Decisions  []string `json:"decisions,omitempty"`
}

const (
	// DefaultQueryLimit is returned when a query sets no limit.
	DefaultQueryLimit = 100
	// MaxQueryLimit is the hard ceiling on a single query's result size, so a
	// remote operator cannot pull an unbounded number of events in one call.
	MaxQueryLimit = 1000
)

// Validate rejects a malformed query fail-loud before any work is done. The
// mode-specific required fields keep an operator honest (e.g. "search" with no
// pattern is almost always a mistake), without constraining the AND-of-filters
// behaviour for callers that set fields directly.
func (q Query) Validate() error {
	switch strings.ToLower(strings.TrimSpace(q.Mode)) {
	case "", "tail":
	case "search":
		if strings.TrimSpace(q.Pattern) == "" {
			return fmt.Errorf("audit query mode=search requires a pattern")
		}
	case "since":
		if q.SinceMS <= 0 {
			return fmt.Errorf("audit query mode=since requires since_ms > 0")
		}
	default:
		return fmt.Errorf("unknown audit query mode %q (want tail|search|since)", q.Mode)
	}
	if q.Limit < 0 {
		return fmt.Errorf("audit query limit must not be negative")
	}
	if q.SinceMS < 0 || q.UntilMS < 0 {
		return fmt.Errorf("audit query time bounds must not be negative")
	}
	if q.SinceMS > 0 && q.UntilMS > 0 && q.SinceMS > q.UntilMS {
		return fmt.Errorf("audit query since_ms (%d) is after until_ms (%d)", q.SinceMS, q.UntilMS)
	}
	return nil
}

func (q Query) limit() int {
	if q.Limit <= 0 {
		return DefaultQueryLimit
	}
	if q.Limit > MaxQueryLimit {
		return MaxQueryLimit
	}
	return q.Limit
}

// Filter applies q to events (which MUST be oldest-first, as ReadAll returns
// them) and returns the matching subset NEWEST-FIRST, capped at the effective
// limit. It never mutates the input.
func Filter(events []Event, q Query) []Event {
	lim := q.limit()
	pat := strings.ToLower(strings.TrimSpace(q.Pattern))
	types := lowerSet(q.EventTypes)
	decisions := lowerSet(q.Decisions)

	// Walk oldest->newest collecting matches; keep only the most recent `lim` by
	// dropping the front once we exceed it (cheap ring-ish trim).
	matches := make([]Event, 0, lim)
	for _, ev := range events {
		if q.SinceMS > 0 && ev.TS < q.SinceMS {
			continue
		}
		if q.UntilMS > 0 && ev.TS > q.UntilMS {
			continue
		}
		if len(types) > 0 && !types[strings.ToLower(ev.EventType)] {
			continue
		}
		if len(decisions) > 0 && !decisions[strings.ToLower(ev.Decision)] {
			continue
		}
		if pat != "" && !eventContains(ev, pat) {
			continue
		}
		matches = append(matches, ev)
		if len(matches) > lim {
			matches = matches[1:] // keep the newest `lim`
		}
	}
	// Reverse to newest-first for display.
	out := make([]Event, len(matches))
	for i, ev := range matches {
		out[len(matches)-1-i] = ev
	}
	return out
}

// eventContains reports whether pat (already lower-cased) appears in any of the
// event's human-meaningful fields. Timestamps and the boolean are intentionally
// excluded — a substring search is for text, not numbers.
func eventContains(ev Event, pat string) bool {
	for _, field := range []string{ev.EventType, ev.CommandID, ev.CommandType, ev.Actor, ev.Source, ev.Decision, ev.Reason, ev.Details} {
		if field != "" && strings.Contains(strings.ToLower(field), pat) {
			return true
		}
	}
	return false
}

func lowerSet(vals []string) map[string]bool {
	if len(vals) == 0 {
		return nil
	}
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			m[v] = true
		}
	}
	return m
}

// ReadAll reads the current audit.log and, when includeRotated is set, the
// rotated audit-<unix>.log files, returning all events OLDEST-FIRST. Rotated
// files are ordered by their embedded rotation timestamp (ascending), with the
// live audit.log last (it holds the newest events). A single unreadable or
// malformed file does not abort the whole read — its partial content is used and
// the first error is returned alongside whatever was parsed, so forensics still
// gets the readable events. The caller passes the audit DIRECTORY; this function
// never reads outside it.
func ReadAll(dir string, includeRotated bool) ([]Event, error) {
	var files []string
	if includeRotated {
		rotated, _ := filepath.Glob(filepath.Join(dir, "audit-*.log"))
		sort.Slice(rotated, func(i, j int) bool { return rotatedTS(rotated[i]) < rotatedTS(rotated[j]) })
		files = append(files, rotated...)
	}
	files = append(files, filepath.Join(dir, "audit.log")) // live file last = newest

	var all []Event
	var firstErr error
	for _, f := range files {
		evs, err := Read(f)
		if err != nil {
			if os.IsNotExist(err) {
				continue // a missing rotated/live file is not an error
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("read %s: %w", filepath.Base(f), err)
			}
		}
		all = append(all, evs...) // Read returns partial content on a parse error
	}
	return all, firstErr
}

// rotatedTS extracts the unix timestamp embedded in an "audit-<unix>.log" name
// for ordering. An unparseable name sorts first (0), which is harmless.
func rotatedTS(path string) int64 {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, ".log")
	base = strings.TrimPrefix(base, "audit-")
	n, _ := strconv.ParseInt(base, 10, 64)
	return n
}
