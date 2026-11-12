package main

import "testing"

func TestDexAgg_AvgMinMax(t *testing.T) {
	var a dexAgg
	if !a.empty() {
		t.Fatal("a fresh aggregator must be empty")
	}
	a.addCPU(10)
	a.addCPU(30)
	a.addCPU(20)
	a.addMem(50)
	a.addMem(70)

	r := a.report("dev-1", 300)
	if r.DeviceId != "dev-1" || r.WindowSeconds != 300 || r.SampleCount != 3 {
		t.Fatalf("header wrong: %+v", r)
	}
	if r.CpuAvg != 20 || r.CpuMin != 10 || r.CpuMax != 30 {
		t.Fatalf("cpu avg/min/max = %v/%v/%v, want 20/10/30", r.CpuAvg, r.CpuMin, r.CpuMax)
	}
	if r.MemUsedAvg != 60 || r.MemUsedMin != 50 || r.MemUsedMax != 70 {
		t.Fatalf("mem avg/min/max = %v/%v/%v, want 60/50/70", r.MemUsedAvg, r.MemUsedMin, r.MemUsedMax)
	}
}

func TestDexAgg_EmptyNoPanic(t *testing.T) {
	var a dexAgg
	r := a.report("dev", 300) // must not divide by zero
	if r.SampleCount != 0 || r.CpuAvg != 0 || r.MemUsedAvg != 0 {
		t.Fatalf("empty report must be zeroed: %+v", r)
	}
}
