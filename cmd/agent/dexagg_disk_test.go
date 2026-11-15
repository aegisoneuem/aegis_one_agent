package main

import "testing"

func TestDexAgg_DiskAggregation(t *testing.T) {
	var a dexAgg
	a.addCPU(10) // keep the window non-empty
	a.addMem(50)
	a.addDisk(diskSample{QueueLength: 1, IOPS: 100, LatencyMs: 2, OK: true})
	a.addDisk(diskSample{QueueLength: 3, IOPS: 300, LatencyMs: 6, OK: true})

	r := a.report("dev", 30)
	if r.DiskQueueAvg != 2 || r.DiskQueueMin != 1 || r.DiskQueueMax != 3 {
		t.Fatalf("queue avg/min/max=%v/%v/%v want 2/1/3", r.DiskQueueAvg, r.DiskQueueMin, r.DiskQueueMax)
	}
	if r.DiskIopsAvg != 200 || r.DiskIopsMin != 100 || r.DiskIopsMax != 300 {
		t.Fatalf("iops avg/min/max=%v/%v/%v want 200/100/300", r.DiskIopsAvg, r.DiskIopsMin, r.DiskIopsMax)
	}
	if r.DiskLatencyMsAvg != 4 || r.DiskLatencyMsMax != 6 {
		t.Fatalf("latency avg/max=%v/%v want 4/6", r.DiskLatencyMsAvg, r.DiskLatencyMsMax)
	}
}

// When no disk sample ever read OK (PDH unavailable, or every tick was governed
// away), the disk fields must stay zero and must not corrupt the cpu/mem report.
func TestDexAgg_NoDiskSamples(t *testing.T) {
	var a dexAgg
	a.addCPU(5)
	a.addMem(40)
	r := a.report("dev", 30)
	if r.DiskQueueAvg != 0 || r.DiskIopsAvg != 0 || r.DiskLatencyMsAvg != 0 {
		t.Fatalf("expected zero disk fields with no disk samples: %+v", r)
	}
	if r.CpuAvg != 5 || r.MemUsedAvg != 40 {
		t.Fatalf("cpu/mem report corrupted: cpu=%v mem=%v", r.CpuAvg, r.MemUsedAvg)
	}
}
