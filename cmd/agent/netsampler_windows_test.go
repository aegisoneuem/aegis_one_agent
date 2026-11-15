//go:build windows

package main

import "testing"

// Loopback is always reachable and needs no gateway/network, so it deterministically
// exercises the ICMP path (handle create → send echo → parse reply → RTT).
func TestNetSampler_Loopback(t *testing.T) {
	n := newNetSampler("127.0.0.1")
	defer n.close()
	if !n.ready {
		t.Fatal("ICMP handle/target setup failed for loopback")
	}
	s := n.sample()
	if !s.OK {
		t.Fatal("loopback sample must be OK")
	}
	if !s.Reachable {
		t.Fatal("loopback must be reachable")
	}
	if s.RttMs < 0 {
		t.Fatalf("negative RTT: %v", s.RttMs)
	}
	t.Logf("loopback rtt=%.0fms", s.RttMs)
}

// Default-gateway resolution + probe. Skips (not fails) in environments without a
// gateway (isolated CI), since that is a valid "no target" case (OK=false).
func TestNetSampler_DefaultGateway(t *testing.T) {
	n := newNetSampler("")
	defer n.close()
	if !n.ready {
		t.Skip("no default gateway resolved in this environment")
	}
	s := n.sample()
	t.Logf("gateway=%s reachable=%v rtt=%.0fms", n.TargetIP, s.Reachable, s.RttMs)
	if !s.OK {
		t.Fatal("sample from a ready sampler must be OK")
	}
}

// close() must be idempotent and safe on a sampler that never became ready.
func TestNetSampler_CloseIdempotent(t *testing.T) {
	n := newNetSampler("127.0.0.1")
	n.close()
	n.close()
	if s := n.sample(); s.OK {
		t.Fatal("sample() after close must not be OK")
	}
}
