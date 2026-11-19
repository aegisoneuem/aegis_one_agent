package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
)

// auditQueryPayload is the JSON body of an audit_query/v1 command. It mirrors the
// agent-side spec (internal/auditlog.Query embedded) so the two never drift.
type auditQueryPayload struct {
	Version        int  `json:"version"`
	IncludeRotated bool `json:"include_rotated,omitempty"`
	auditlog.Query
}

// verbAudit queries a target's LOCAL audit trace from the console:
//
//	audit tail [N]                 — the N most recent events (default 20)
//	audit search <text> [N]        — events whose text matches (case-insensitive)
//	audit since <unix_ms> [N]      — events at/after a timestamp
//
// Append `rotated` to also scan rotated audit-<ts>.log files. The agent returns
// metadata only (no secrets); this renders it one event per line, newest-first.
func (s *svshSession) verbAudit(rest string) {
	if len(s.targets) == 0 {
		s.printf("svsh: no target selected — run `use <host>` first\n")
		return
	}
	p := auditQueryPayload{Version: 1, Query: auditlog.Query{Mode: "tail", Limit: 20}}

	// Pull out the optional `rotated` keyword wherever it appears.
	var pos []string
	for _, f := range strings.Fields(rest) {
		if strings.EqualFold(f, "rotated") {
			p.IncludeRotated = true
			continue
		}
		pos = append(pos, f)
	}
	if len(pos) > 0 {
		switch strings.ToLower(pos[0]) {
		case "tail":
			p.Mode = "tail"
			if len(pos) > 1 {
				p.Limit = auditAtoi(pos[1], p.Limit)
			}
		case "search":
			if len(pos) < 2 {
				s.printf("usage: audit search <text> [N] [rotated]\n")
				return
			}
			p.Mode = "search"
			p.Pattern = pos[1]
			if len(pos) > 2 {
				p.Limit = auditAtoi(pos[2], p.Limit)
			}
		case "since":
			if len(pos) < 2 {
				s.printf("usage: audit since <unix_ms> [N] [rotated]\n")
				return
			}
			ms, err := strconv.ParseInt(pos[1], 10, 64)
			if err != nil {
				s.printf("svsh: `audit since` needs a unix-millis timestamp\n")
				return
			}
			p.Mode = "since"
			p.SinceMS = ms
			if len(pos) > 2 {
				p.Limit = auditAtoi(pos[2], p.Limit)
			}
		default:
			s.printf("usage: audit tail [N] | audit search <text> [N] | audit since <unix_ms> [N]  (append `rotated` for rotated files)\n")
			return
		}
	}
	if err := p.Query.Validate(); err != nil {
		s.printf("svsh: %v\n", err)
		return
	}
	payload, err := json.Marshal(p)
	if err != nil {
		s.printf("svsh: marshal: %v\n", err)
		return
	}

	for _, devID := range s.targets {
		host := s.srv.hostnameOf(devID)
		st, _ := s.sendTaskPayload(devID, "audit_query", payload, "")
		if st == nil {
			continue
		}
		s.printf("=== audit @ %s — %s ===\n", host, st.Reason)
		renderAuditDetail(s, st.Detail)
	}
}

// renderAuditDetail parses the ActionStatus.Detail event array and prints it.
func renderAuditDetail(s *svshSession, detail string) {
	if strings.TrimSpace(detail) == "" {
		s.printf("  (no matching events)\n")
		return
	}
	var evs []auditlog.Event
	if err := json.Unmarshal([]byte(detail), &evs); err != nil {
		s.printf("  (could not parse audit detail: %v)\n", err)
		return
	}
	if len(evs) == 0 {
		s.printf("  (no matching events)\n")
		return
	}
	for _, e := range evs {
		s.printf("  %s  %-18s %-10s %-16s %-22s %s\n",
			auditStamp(e.TS), e.EventType, e.Decision, e.CommandID, e.Actor, e.Reason)
	}
}

// printAuditEvents renders an audit_query ActionStatus.Detail (a JSON event
// array) for the standalone, non-SVSH receive loop. Used by the AEGIS_AUDIT_QUERY
// env trigger so a tester sees readable rows, not raw JSON.
func printAuditEvents(detail string) {
	if strings.TrimSpace(detail) == "" {
		mockPrintf("  (no matching events)\n")
		return
	}
	var evs []auditlog.Event
	if err := json.Unmarshal([]byte(detail), &evs); err != nil {
		mockPrintf("  detail: %s\n", detail) // fall back to raw on a parse miss
		return
	}
	if len(evs) == 0 {
		mockPrintf("  (no matching events)\n")
		return
	}
	for _, e := range evs {
		mockPrintf("  %s  %-18s %-10s %-16s %-22s %s\n",
			auditStamp(e.TS), e.EventType, e.Decision, e.CommandID, e.Actor, e.Reason)
	}
}

func auditAtoi(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}

func auditStamp(ms int64) string {
	if ms <= 0 {
		return "----------------   "
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04:05")
}
