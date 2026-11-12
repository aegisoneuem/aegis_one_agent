//go:build windows

package main

import (
	"testing"
	"time"
)

func TestResourceSampler_RealReadings(t *testing.T) {
	s := newResourceSampler()
	s.sample() // prime the CPU baseline
	time.Sleep(60 * time.Millisecond)

	cpu, cpuOK, mem, memOK := s.sample()
	if !memOK {
		t.Fatal("memory read must succeed")
	}
	if mem < 0 || mem > 100 {
		t.Fatalf("mem_used out of range: %v", mem)
	}
	if cpuOK && (cpu < 0 || cpu > 100) {
		t.Fatalf("cpu out of range: %v", cpu)
	}
	t.Logf("cpu=%.1f%% (ok=%v) mem_used=%.1f%%", cpu, cpuOK, mem)
}
