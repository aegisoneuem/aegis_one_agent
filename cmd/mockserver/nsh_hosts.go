package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type hostQuery struct {
	filter, status string
	page, limit    int
}
type hostRow struct{ host, id, cid, status string }

func parseHostQuery(rest string) (hostQuery, error) {
	q := hostQuery{page: 1, limit: 20, status: "all"}
	args, err := splitNSHArgs(rest)
	if err != nil {
		return q, err
	}
	var filters []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--page" || a == "--limit" || a == "--status" {
			if i+1 >= len(args) {
				return q, fmt.Errorf("%s needs a value", a)
			}
			i++
			value := args[i]
			switch a {
			case "--page":
				q.page, err = strconv.Atoi(value)
			case "--limit":
				q.limit, err = strconv.Atoi(value)
			case "--status":
				q.status = strings.ToLower(value)
			}
			if err != nil {
				return q, err
			}
		} else if strings.HasPrefix(a, "--") {
			return q, fmt.Errorf("unknown hosts option %s", a)
		} else {
			filters = append(filters, a)
		}
	}
	if q.page < 1 || q.limit < 1 || q.limit > 100 || q.page > 1_000_000 {
		return q, fmt.Errorf("page must be 1..1000000; limit must be 1..100")
	}
	if q.status != "all" && q.status != "online" && q.status != "offline" {
		return q, fmt.Errorf("status must be all|online|offline")
	}
	q.filter = strings.Join(filters, " ")
	return q, nil
}
func (s *server) hostPage(q hostQuery) ([]hostRow, int) {
	return s.hostPageScoped(q, nil)
}
func (s *server) hostPageScoped(q hostQuery, allowed map[string]bool) ([]hostRow, int) {
	s.liveMu.Lock()
	online := make(map[string]bool, len(s.liveAgents))
	for id := range s.liveAgents {
		online[id] = true
	}
	s.liveMu.Unlock()
	var rows []hostRow
	s.mu.Lock()
	for id, r := range s.byID {
		if allowed != nil && !allowed[id] {
			continue
		}
		status := "OFFLINE"
		if online[id] {
			status = "ONLINE"
		}
		if q.status != "all" && !strings.EqualFold(q.status, status) {
			continue
		}
		if q.filter != "" && !matchFilter(r.Hostname, id, r.ComputerID, q.filter) {
			continue
		}
		rows = append(rows, hostRow{host: r.Hostname, id: id, cid: r.ComputerID, status: status})
	}
	s.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].host == rows[j].host {
			return rows[i].id < rows[j].id
		}
		return rows[i].host < rows[j].host
	})
	total := len(rows)
	start := (q.page - 1) * q.limit
	if start >= total {
		return nil, total
	}
	end := start + q.limit
	if end > total {
		end = total
	}
	return rows[start:end], total
}
func (s *nshSession) printHosts(rest string) {
	q, err := parseHostQuery(rest)
	if err != nil {
		s.println("nsh:", err)
		return
	}
	rows, total := s.srv.hostPageScoped(q, s.allowedTargets)
	s.printf("%-18s  %-36s  %-12s  %s\n", "HOST", "DEVICE_ID", "COMPUTER_ID", "STATUS")
	for _, r := range rows {
		s.printf("%-18s  %-36s  %-12s  %s\n", r.host, r.id, r.cid, r.status)
	}
	s.printf("Showing %d of %d match(es), page %d (limit %d).\n", len(rows), total, q.page, q.limit)
	if q.page*q.limit < total {
		s.printf("Next: hosts %s --status %s --page %d --limit %d\n", q.filter, q.status, q.page+1, q.limit)
	}
	if len(s.targets) == 1 {
		s.printf("Selected: %s (%s)\n", s.srv.hostnameOf(s.targets[0]), map[bool]string{true: "ONLINE", false: "OFFLINE"}[s.srv.isOnline(s.targets[0])])
	}
}
