//go:build linux

package main

import (
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// collectIdentityInfo gathers Linux session/identity attributes: the logged-on user
// (first active session via `who`), system uptime (/proc/uptime), and the NIS/DNS
// domain. AD domain-join detection (realmd/sssd) is a later enhancement — DomainJoined
// stays false for now. On a headless server with no interactive login, LoggedOnUser is
// legitimately empty.
func collectIdentityInfo(collectedAt int64) *pb.IdentityInfo {
	id := &pb.IdentityInfo{}
	id.LoggedOnUser = firstLoggedOnUser()
	if up := uptimeSeconds(); up > 0 {
		id.UptimeSeconds = up
		id.BootTime = collectedAt - up
	}
	id.MachineDomain = linuxDomain()
	return id
}

func uptimeSeconds() int64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	s, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return int64(s)
}

// firstLoggedOnUser returns the NAME of the first user with an active session. Prefers
// systemd loginctl (UID column → resolve to name, column-order-safe); falls back to
// `who`. Some minimal images' `who` prints a UID, so any numeric value is resolved to
// the username. Empty when no one is logged in (normal on a headless server).
func firstLoggedOnUser() string {
	// loginctl list-sessions: SESSION UID USER SEAT TTY — UID (f[1]) is always numeric.
	if out, err := exec.Command("loginctl", "list-sessions", "--no-legend").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if f := strings.Fields(line); len(f) >= 2 {
				if name := resolveUserName(f[1]); name != "" {
					return name
				}
			}
		}
	}
	if out, err := exec.Command("who").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if f := strings.Fields(line); len(f) > 0 {
				return resolveUserName(f[0])
			}
		}
	}
	return ""
}

// resolveUserName turns a numeric UID into its username; passes a name through unchanged.
func resolveUserName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if _, err := strconv.Atoi(s); err == nil { // numeric → a UID
		if u, e := user.LookupId(s); e == nil && u.Username != "" {
			return u.Username
		}
	}
	return s
}

func linuxDomain() string {
	if b, err := os.ReadFile("/proc/sys/kernel/domainname"); err == nil {
		if d := strings.TrimSpace(string(b)); d != "" && d != "(none)" {
			return d
		}
	}
	return ""
}
