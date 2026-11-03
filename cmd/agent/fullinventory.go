package main

import (
	"log/slog"
	"sync"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/buffer"
	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	"github.com/aegisoneuem/aegis_one_agent/internal/hwinventory"
	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

// runFullInventoryReport is the Tier-3 (6-12 h) safety-net report: on a
// slow timer AND on reconnect-after-offline, it collects the full HW + SW
// snapshot and buffers it for the sender. Even if a Tier-2 change event
// is missed (race, agent restart, bug), one Tier-3 cycle self-corrects
// the server's view.
//
// Reconnect-triggered snapshots carry a FLAP FLOOR: after a reconnect
// snapshot is sent, further reconnect triggers within
// cfg.ReconnectSnapshotMinInterval are DROPPED so a flapping NIC does
// not DoS the server. The scheduled 6-12 h timer is NEVER suppressed by
// the floor.
//
// Collector cost is a few ms (no WMI, no COM). RAM impact is one
// transient Inventory struct (dropped after marshal). Both acceptable
// on the lightweight-agent budget Vijay set (binary <= 25 MB,
// low CPU/RAM).
func runFullInventoryReport(cfg config.Config, buf buffer.Buffer, reconnectTrigger <-chan struct{}, drainSignal chan<- struct{}) {
	var mu sync.Mutex
	var lastReconnectSend time.Time

	// snapshot assembles one full inventory report. trigger is "scheduled"
	// or "reconnect" so the server can see WHY the snapshot ran.
	snapshot := func(trigger string) {
		started := time.Now()
		hw, err := hwinventory.Collect()
		if err != nil {
			slog.Error("TIER-3 full-inventory: HW collect failed", "error", err.Error(), "trigger", trigger)
			return
		}
		apps, aerr := softdist.EnumerateInstalledApps()
		if aerr != nil {
			slog.Warn("TIER-3 full-inventory: SW enumerate failed (continuing with empty list)", "error", aerr.Error(), "trigger", trigger)
			apps = nil
		}
		rep := &pb.FullInventoryReport{
			DeviceId:    cfg.DeviceID,
			CollectedAt: time.Now().Unix(),
			Trigger:     trigger,
			Hardware:    toProtoHardware(hw),
		}
		rep.InstalledApps = make([]*pb.InstalledApp, 0, len(apps))
		for _, a := range apps {
			rep.InstalledApps = append(rep.InstalledApps, &pb.InstalledApp{
				DisplayName:     a.DisplayName,
				DisplayVersion:  a.DisplayVersion,
				Publisher:       a.Publisher,
				InstallDate:     a.InstallDate,
				UninstallKey:    a.Key,
				Hive:            a.RegistryView,
				EstimatedSizeKb: a.EstimatedSizeKB,
				UninstallString: a.UninstallString,
			})
		}
		payload, err := proto.Marshal(rep)
		if err != nil {
			slog.Error("TIER-3 full-inventory: marshal failed", "error", err.Error())
			return
		}
		if err := buf.Enqueue(kindFullInventory, payload); err != nil {
			slog.Error("TIER-3 full-inventory: buffer enqueue failed", "error", err.Error())
			return
		}
		slog.Info("TIER-3 full-inventory: snapshot buffered",
			"trigger", trigger,
			"apps", len(rep.InstalledApps),
			"disks", len(rep.GetHardware().GetDisks()),
			"nics", len(rep.GetHardware().GetNics()),
			"cpu_model", rep.GetHardware().GetCpuModel(),
			"ram_bytes", rep.GetHardware().GetMemoryTotalBytes(),
			"took_ms", time.Since(started).Milliseconds())
		signalDrain(drainSignal)
	}

	// First snapshot at startup — covers the case where the agent is
	// freshly installed or has been down for a long time.
	safeRun("tier3-full-inventory-startup", func() { snapshot("startup") })

	ticker := time.NewTicker(cfg.FullInventoryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			safeRun("tier3-full-inventory-scheduled", func() { snapshot("scheduled") })

		case <-reconnectTrigger:
			// Flap floor: suppress a reconnect snapshot if the last one
			// was within ReconnectSnapshotMinInterval. The scheduled
			// 6-12 h timer is unaffected.
			mu.Lock()
			age := time.Since(lastReconnectSend)
			within := age < cfg.ReconnectSnapshotMinInterval
			mu.Unlock()
			if within {
				slog.Debug("TIER-3 full-inventory: reconnect trigger suppressed by flap floor",
					"age", age.String(), "floor", cfg.ReconnectSnapshotMinInterval.String())
				continue
			}
			safeRun("tier3-full-inventory-reconnect", func() { snapshot("reconnect") })
			mu.Lock()
			lastReconnectSend = time.Now()
			mu.Unlock()
		}
	}
}

// toProtoHardware maps the hwinventory.Inventory struct onto the proto
// HardwareInventory message. Keeps the HW collector package independent
// of the proto types (easier to unit test).
func toProtoHardware(inv *hwinventory.Inventory) *pb.HardwareInventory {
	if inv == nil {
		return nil
	}
	hw := &pb.HardwareInventory{
		CpuVendor:          inv.CPUVendor,
		CpuModel:           inv.CPUModel,
		CpuPhysicalCores:   inv.CPUPhysicalCores,
		CpuLogicalCores:    inv.CPULogicalCores,
		MemoryTotalBytes:   inv.MemoryTotalBytes,
		BiosVendor:         inv.BIOSVendor,
		BiosVersion:        inv.BIOSVersion,
		BiosReleaseDate:    inv.BIOSReleaseDate,
		SystemVendor:       inv.SystemVendor,
		SystemProduct:      inv.SystemProduct,
		SystemSerialNumber: inv.SystemSerialNumber,
		ChassisType:        inv.ChassisType,
		CpuMhz:             inv.CPUMHz,
		CpuThreadsPerCore:  inv.CPUThreadsPerCore,
		CpuArchitecture:    inv.CPUArchitecture,
		OsProductName:      inv.OSProductName,
		OsDisplayVersion:   inv.OSDisplayVersion,
		OsBuild:            inv.OSBuild,
		OsServicePack:      inv.OSServicePack,
		SystemUuid:         inv.SystemUUID,
		Virtualization:     inv.Virtualization,
	}
	hw.Disks = make([]*pb.DiskInventory, 0, len(inv.Disks))
	for _, d := range inv.Disks {
		hw.Disks = append(hw.Disks, &pb.DiskInventory{
			MountPoint:  d.MountPoint,
			VolumeLabel: d.VolumeLabel,
			FileSystem:  d.FileSystem,
			TotalBytes:  d.TotalBytes,
			FreeBytes:   d.FreeBytes,
			DriveType:   d.DriveType,
		})
	}
	hw.Nics = make([]*pb.NetworkInterface, 0, len(inv.NICs))
	for _, n := range inv.NICs {
		hw.Nics = append(hw.Nics, &pb.NetworkInterface{
			Name:          n.Name,
			MacAddress:    n.MACAddress,
			Ipv4Addresses: n.IPv4Addresses,
			Ipv6Addresses: n.IPv6Addresses,
			LinkSpeedBps:  n.LinkSpeedBPS,
			Status:        n.Status,
		})
	}
	return hw
}
