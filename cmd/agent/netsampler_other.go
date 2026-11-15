//go:build !windows

package main

// Non-Windows stub: the network sampler uses the Windows ICMP helper. sample()
// yields OK=false so callers omit network metrics on other platforms.
type netSample struct {
	RttMs     float64
	Reachable bool
	OK        bool
}

type netSampler struct{ TargetIP string }

func newNetSampler(override string) *netSampler { return &netSampler{} }
func (n *netSampler) sample() netSample          { return netSample{} }
func (n *netSampler) close()                     {}
