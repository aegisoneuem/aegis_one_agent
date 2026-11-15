//go:build windows

package main

import (
	"testing"
	"time"
)

// Exercises the real PDH PhysicalDisk counters. Rate counters need a delta, so we
// prime (constructor) then sample after a short interval. If the perf-counter
// subsystem is unavailable in the environment, the sampler yields OK=false and the
// test skips rather than failing.
func TestDiskSampler_RealCounters(t *testing.T) {
	d := newDiskSampler()
	defer d.close()

	time.Sleep(250 * time.Millisecond) // let the rate counters accumulate a delta
	s := d.sample()
	if !s.OK {
		t.Skip("PDH PhysicalDisk(_Total) counters unavailable here — disk metrics will be omitted")
	}
	if s.QueueLength < 0 || s.IOPS < 0 || s.LatencyMs < 0 {
		t.Fatalf("disk metrics must be non-negative: %+v", s)
	}
	t.Logf("disk: queue=%.3f iops=%.1f latency_ms=%.3f", s.QueueLength, s.IOPS, s.LatencyMs)
}

// close() must be safe to call repeatedly (idempotent) and must not panic on a
// sampler that failed to initialize.
func TestDiskSampler_CloseIdempotent(t *testing.T) {
	d := newDiskSampler()
	d.close()
	d.close() // second call must be a no-op
	if s := d.sample(); s.OK {
		t.Fatal("sample() after close must not report OK")
	}
}
