//go:build linux

package main

import (
	"os"
	"os/exec"
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

// firstLoggedOnUser returns the first user with an active session (`who`), or "" when
// no one is logged in.
func firstLoggedOnUser() string {
	out, err := exec.Command("who").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			return f[0]
		}
	}
	return ""
}

func linuxDomain() string {
	if b, err := os.ReadFile("/proc/sys/kernel/domainname"); err == nil {
		if d := strings.TrimSpace(string(b)); d != "" && d != "(none)" {
			return d
		}
	}
	return ""
}
