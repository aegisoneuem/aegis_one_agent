//go:build windows

package main

import (
	"testing"
	"time"
)

func TestCollectIdentityInfo(t *testing.T) {
	now := time.Now().Unix()
	id := collectIdentityInfo(now)
	if id == nil {
		t.Fatal("nil identity")
	}
	if id.UptimeSeconds <= 0 {
		t.Fatalf("uptime must be positive, got %d", id.UptimeSeconds)
	}
	if id.BootTime <= 0 || id.BootTime > now {
		t.Fatalf("boot_time must be a past unix time, got %d (now %d)", id.BootTime, now)
	}
	if id.BootTime != now-id.UptimeSeconds {
		t.Fatalf("boot_time (%d) must equal collectedAt-uptime (%d)", id.BootTime, now-id.UptimeSeconds)
	}
	// machine_domain is a workgroup or AD domain name on a real host; logged-on
	// user may be empty in a non-interactive test/service context. Both are
	// best-effort, so log rather than assert their presence.
	t.Logf("identity: user=%q domain=%q joined=%v uptime=%ds", id.LoggedOnUser, id.MachineDomain, id.DomainJoined, id.UptimeSeconds)
}
