package main

import "testing"

// 4 reachable probes (RTT 2/4/6/8) + 1 loss → rtt avg=5 min=2 max=8, loss=20%.
func TestDexAgg_NetAggregation(t *testing.T) {
	var a dexAgg
	a.addCPU(1)
	a.netTarget = "192.168.1.1"
	a.addNet(netSample{RttMs: 2, Reachable: true, OK: true})
	a.addNet(netSample{RttMs: 4, Reachable: true, OK: true})
	a.addNet(netSample{RttMs: 6, Reachable: true, OK: true})
	a.addNet(netSample{RttMs: 8, Reachable: true, OK: true})
	a.addNet(netSample{Reachable: false, OK: true}) // one lost probe
	a.addNet(netSample{OK: false})                  // no target that tick — ignored

	r := a.report("dev", 30)
	if r.NetRttMsAvg != 5 || r.NetRttMsMin != 2 || r.NetRttMsMax != 8 {
		t.Fatalf("rtt avg/min/max=%v/%v/%v want 5/2/8", r.NetRttMsAvg, r.NetRttMsMin, r.NetRttMsMax)
	}
	if r.NetLossPct != 20 {
		t.Fatalf("loss=%v%% want 20 (1 of 5 attempts)", r.NetLossPct)
	}
	if r.NetTarget != "192.168.1.1" {
		t.Fatalf("target=%q want 192.168.1.1", r.NetTarget)
	}
}

// All probes lost → loss=100%, rtt stays 0 (no reachable samples), target reported.
func TestDexAgg_NetAllLoss(t *testing.T) {
	var a dexAgg
	a.addCPU(1)
	a.netTarget = "10.0.0.1"
	a.addNet(netSample{Reachable: false, OK: true})
	a.addNet(netSample{Reachable: false, OK: true})
	r := a.report("dev", 30)
	if r.NetLossPct != 100 {
		t.Fatalf("loss=%v%% want 100", r.NetLossPct)
	}
	if r.NetRttMsAvg != 0 {
		t.Fatalf("rtt avg=%v want 0 (no reachable probes)", r.NetRttMsAvg)
	}
}

// No network samples at all → net fields stay zero, cpu untouched.
func TestDexAgg_NetNoSamples(t *testing.T) {
	var a dexAgg
	a.addCPU(7)
	r := a.report("dev", 30)
	if r.NetRttMsAvg != 0 || r.NetLossPct != 0 || r.NetTarget != "" {
		t.Fatalf("expected empty net fields: %+v", r)
	}
}
