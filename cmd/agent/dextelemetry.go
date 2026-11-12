package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/buffer"
	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

// dexAgg accumulates CPU and memory samples over a window into avg/min/max.
type dexAgg struct {
	cpuN               int
	cpuSum, cpuMin, cpuMax float64
	memN               int
	memSum, memMin, memMax float64
}

func (a *dexAgg) addCPU(v float64) {
	if a.cpuN == 0 || v < a.cpuMin {
		a.cpuMin = v
	}
	if a.cpuN == 0 || v > a.cpuMax {
		a.cpuMax = v
	}
	a.cpuSum += v
	a.cpuN++
}

func (a *dexAgg) addMem(v float64) {
	if a.memN == 0 || v < a.memMin {
		a.memMin = v
	}
	if a.memN == 0 || v > a.memMax {
		a.memMax = v
	}
	a.memSum += v
	a.memN++
}

func (a *dexAgg) empty() bool { return a.cpuN == 0 && a.memN == 0 }

func (a *dexAgg) report(deviceID string, windowSec int) *pb.DexTelemetry {
	r := &pb.DexTelemetry{
		DeviceId: deviceID, CollectedAt: time.Now().Unix(),
		WindowSeconds: int32(windowSec), SampleCount: int32(a.cpuN),
	}
	if a.cpuN > 0 {
		r.CpuAvg, r.CpuMin, r.CpuMax = a.cpuSum/float64(a.cpuN), a.cpuMin, a.cpuMax
	}
	if a.memN > 0 {
		r.MemUsedAvg, r.MemUsedMin, r.MemUsedMax = a.memSum/float64(a.memN), a.memMin, a.memMax
	}
	return r
}

// runDexTelemetry samples live CPU + memory on cfg.DexSampleInterval and, every
// cfg.DexWindow, buffers one aggregated DexTelemetry report for the sender. It
// runs on the ROOT context (one sampler for the agent's life, across reconnects)
// and buffers like any other report so a window is not lost while offline. It is
// deliberately OUTSIDE the change-gated inventory path: CPU/memory change every
// cycle, so folding them into inventory would force a send every tick.
func runDexTelemetry(ctx context.Context, cfg config.Config, buf buffer.Buffer, drainSignal chan<- struct{}) {
	defer recoverLog("dex-telemetry")
	sampler := newResourceSampler()
	sampler.sample() // prime the CPU baseline so the first real sample has a delta

	sampleTick := time.NewTicker(cfg.DexSampleInterval)
	defer sampleTick.Stop()
	windowTick := time.NewTicker(cfg.DexWindow)
	defer windowTick.Stop()

	var agg dexAgg
	for {
		select {
		case <-ctx.Done():
			return
		case <-sampleTick.C:
			cpu, cpuOK, mem, memOK := sampler.sample()
			if cpuOK {
				agg.addCPU(cpu)
			}
			if memOK {
				agg.addMem(mem)
			}
		case <-windowTick.C:
			if agg.empty() {
				continue
			}
			rep := agg.report(cfg.DeviceID, int(cfg.DexWindow.Seconds()))
			agg = dexAgg{}
			payload, err := proto.Marshal(rep)
			if err != nil {
				continue
			}
			if err := buf.Enqueue(kindDexTelemetry, payload); err != nil {
				slog.Warn("dex-telemetry: enqueue failed", "error", err.Error())
				continue
			}
			slog.Info("dex-telemetry: window buffered", "samples", rep.SampleCount,
				"cpu_avg", rep.CpuAvg, "mem_used_avg", rep.MemUsedAvg)
			signalDrain(drainSignal)
		}
	}
}
