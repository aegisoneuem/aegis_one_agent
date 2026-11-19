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

// stat accumulates samples of one metric into avg/min/max over a window.
type stat struct {
	n             int
	sum, min, max float64
}

func (s *stat) add(v float64) {
	if s.n == 0 || v < s.min {
		s.min = v
	}
	if s.n == 0 || v > s.max {
		s.max = v
	}
	s.sum += v
	s.n++
}

func (s *stat) avg() float64 {
	if s.n == 0 {
		return 0
	}
	return s.sum / float64(s.n)
}

// dexAgg accumulates CPU, memory, and disk samples over a window into avg/min/max.
type dexAgg struct {
	cpuN                   int
	cpuSum, cpuMin, cpuMax float64
	memN                   int
	memSum, memMin, memMax float64
	// Disk performance (PhysicalDisk _Total); aggregated only when a disk sample
	// reads OK, so it may hold fewer samples than cpu/mem (0 when PDH unavailable).
	diskQueue, diskIOPS, diskLat stat
	// Network latency/reachability to the ping target. netRtt holds RTTs of the
	// probes that got a reply; netAttempts/netLosses yield packet-loss %.
	netRtt                 stat
	netAttempts, netLosses int
	netTarget              string
	// Battery (laptops). batterySeen gates reporting; present/acOnline hold the last
	// sampled state; batteryCharge tracks charge % avg/min/max (drain) over the window.
	batterySeen                 bool
	batteryPresent, batteryACOn bool
	batteryCharge               stat
	// Wi-Fi (laptops). present/connected/ssid are last-value; wifiSignal tracks
	// signal-quality % avg/min/max over the window.
	wifiSeen                  bool
	wifiPresent, wifiConnected bool
	wifiSignal                stat
	wifiSSID                  string
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

func (a *dexAgg) addDisk(s diskSample) {
	a.diskQueue.add(s.QueueLength)
	a.diskIOPS.add(s.IOPS)
	a.diskLat.add(s.LatencyMs)
}

func (a *dexAgg) addNet(s netSample) {
	if !s.OK {
		return
	}
	a.netAttempts++
	if s.Reachable {
		a.netRtt.add(s.RttMs)
	} else {
		a.netLosses++
	}
}

func (a *dexAgg) addBattery(b batterySample) {
	if !b.OK {
		return
	}
	a.batterySeen = true
	a.batteryPresent = b.Present
	a.batteryACOn = b.ACOnline
	if b.Present && b.ChargePct >= 0 {
		a.batteryCharge.add(float64(b.ChargePct))
	}
}

func (a *dexAgg) addWifi(w wifiSample) {
	if !w.OK {
		return
	}
	a.wifiSeen = true
	a.wifiPresent = w.Present
	a.wifiConnected = w.Connected
	if w.SSID != "" {
		a.wifiSSID = w.SSID
	}
	if w.Connected {
		a.wifiSignal.add(float64(w.SignalPct))
	}
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
	if a.diskQueue.n > 0 {
		r.DiskQueueAvg, r.DiskQueueMin, r.DiskQueueMax = a.diskQueue.avg(), a.diskQueue.min, a.diskQueue.max
		r.DiskIopsAvg, r.DiskIopsMin, r.DiskIopsMax = a.diskIOPS.avg(), a.diskIOPS.min, a.diskIOPS.max
		r.DiskLatencyMsAvg, r.DiskLatencyMsMin, r.DiskLatencyMsMax = a.diskLat.avg(), a.diskLat.min, a.diskLat.max
	}
	if a.netAttempts > 0 {
		if a.netRtt.n > 0 {
			r.NetRttMsAvg, r.NetRttMsMin, r.NetRttMsMax = a.netRtt.avg(), a.netRtt.min, a.netRtt.max
		}
		r.NetLossPct = float64(a.netLosses) / float64(a.netAttempts) * 100
		r.NetTarget = a.netTarget
	}
	if a.batterySeen {
		r.BatteryPresent = a.batteryPresent
		r.BatteryAcOnline = a.batteryACOn
		if a.batteryCharge.n > 0 {
			r.BatteryChargePctAvg, r.BatteryChargePctMin, r.BatteryChargePctMax = a.batteryCharge.avg(), a.batteryCharge.min, a.batteryCharge.max
		}
		// Wear/health is static over the window — read it once here.
		if a.batteryPresent {
			if health, cycles, ok := readBatteryWear(); ok {
				r.BatteryHealthPct = health
				r.BatteryCycleCount = int32(cycles)
			}
		}
	}
	if a.wifiSeen {
		r.WifiPresent = a.wifiPresent
		r.WifiConnected = a.wifiConnected
		r.WifiSsid = a.wifiSSID
		if a.wifiSignal.n > 0 {
			r.WifiSignalPctAvg, r.WifiSignalPctMin, r.WifiSignalPctMax = a.wifiSignal.avg(), a.wifiSignal.min, a.wifiSignal.max
		}
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
	disk := newDiskSampler()
	defer disk.close()
	netS := newNetSampler(cfg.DexPingTarget)
	defer netS.close()
	netTargetIP := netS.TargetIP

	sampleTick := time.NewTicker(cfg.DexSampleInterval)
	defer sampleTick.Stop()
	windowTick := time.NewTicker(cfg.DexWindow)
	defer windowTick.Stop()

	var agg dexAgg
	busySkipped := 0 // sample ticks dropped this window because the agent was busy
	for {
		select {
		case <-ctx.Done():
			return
		case <-sampleTick.C:
			// Always sample so the CPU-delta baseline stays current; otherwise the
			// first reading AFTER a patch/deploy would span the busy period and be
			// inflated. We advance the baseline either way, and only aggregate when
			// the agent is idle.
			cpu, cpuOK, mem, memOK := sampler.sample()
			ds := disk.sample() // advances the PDH rate-counter baseline too
			ns := netS.sample()
			bs := readBattery()
			ws := readWifi()
			if heavyWork.Busy() {
				// Resource-governor: the agent is mid patch-install / software-deploy.
				// Drop this reading so DEX reflects the user's device experience, not
				// the agent's own install load — and so we add no contention while the
				// real work runs.
				busySkipped++
				continue
			}
			if cpuOK {
				agg.addCPU(cpu)
			}
			if memOK {
				agg.addMem(mem)
			}
			if ds.OK {
				agg.addDisk(ds)
			}
			if ns.OK {
				agg.addNet(ns)
				agg.netTarget = netTargetIP
			}
			agg.addBattery(bs)
			agg.addWifi(ws)
		case <-windowTick.C:
			skipped := busySkipped
			busySkipped = 0
			if agg.empty() {
				if skipped > 0 {
					slog.Info("dex-telemetry: window skipped — agent busy the whole window", "busy_ticks", skipped)
				}
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
				"busy_skipped", skipped, "cpu_avg", rep.CpuAvg, "mem_used_avg", rep.MemUsedAvg,
				"disk_q_avg", rep.DiskQueueAvg, "disk_iops_avg", rep.DiskIopsAvg, "disk_lat_ms_avg", rep.DiskLatencyMsAvg,
				"net_rtt_ms_avg", rep.NetRttMsAvg, "net_loss_pct", rep.NetLossPct, "net_target", rep.NetTarget,
				"batt_present", rep.BatteryPresent, "batt_ac", rep.BatteryAcOnline, "batt_charge_avg", rep.BatteryChargePctAvg,
				"wifi_present", rep.WifiPresent, "wifi_signal_avg", rep.WifiSignalPctAvg, "wifi_ssid", rep.WifiSsid)
			signalDrain(drainSignal)
		}
	}
}
