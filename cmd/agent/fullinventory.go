package main

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/appxinventory"
	"github.com/aegisoneuem/aegis_one_agent/internal/buffer"
	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	"github.com/aegisoneuem/aegis_one_agent/internal/hwinventory"
	"github.com/aegisoneuem/aegis_one_agent/internal/patch"
	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

// fullInventoryFingerprint hashes the STRUCTURAL fields of a
// FullInventoryReport — the parts that only change when the hardware or
// installed software actually changes. Operational / fluctuating values
// (collected_at, trigger, disk free bytes, DHCP IPs, link status / speed)
// are zeroed out before hashing: they vary every poll and would otherwise
// trigger "drift" sends on no real change.
//
// Values are still SENT to the server in the full report — this hash only
// controls whether a send is suppressed as unchanged.
func fullInventoryFingerprint(rep *pb.FullInventoryReport) string {
	snap := proto.Clone(rep).(*pb.FullInventoryReport)
	// Always-noisy top-level fields.
	snap.CollectedAt = 0
	snap.Trigger = ""
	// Disk free space fluctuates every poll (Windows caches, logs, service
	// temp files allocate / free constantly). Zero it for the fingerprint;
	// total size + drive type + file system are the stable structural keys.
	for _, d := range snap.GetHardware().GetDisks() {
		if d != nil {
			d.FreeBytes = 0
		}
	}
	// NIC addresses and link state are operational. The adapter's name +
	// MAC are the stable physical identity; link flap / DHCP renewal must
	// not fire inventory drift.
	for _, n := range snap.GetHardware().GetNics() {
		if n != nil {
			n.Ipv4Addresses = nil
			n.Ipv6Addresses = nil
			n.LinkSpeedBps = 0
			n.Status = ""
		}
	}
	bytes, err := proto.Marshal(snap)
	if err != nil {
		// Marshal of a proto we just built should never fail. Return a
		// unique value so the "unchanged" path cannot accidentally match.
		return "marshal-error-" + time.Now().UTC().Format(time.RFC3339Nano)
	}
	sum := sha256.Sum256(bytes)
	return hex.EncodeToString(sum[:])
}

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
	// Send-on-change state: skip identical reports until the safety-net
	// interval elapses. Startup and reconnect ALWAYS send (bypass the
	// change check) because the server needs the authoritative snapshot
	// for a fresh/returning device.
	var lastFingerprint string
	var lastSentAt time.Time

	// snapshot assembles one full inventory report. trigger is "startup",
	// "scheduled", or "reconnect" so the server can see WHY the snapshot
	// ran. forceSend bypasses the send-on-change check (used by startup
	// and reconnect so the server gets the authoritative state after a
	// device appears or reappears).
	snapshot := func(trigger string, forceSend bool) {
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
		// Driver inventory for OEM driver patching. Best-effort — a WMI
		// failure here must NOT drop the whole snapshot (HW + apps still
		// ship). The server catalog decides which drivers are patchable.
		if drivers, derr := patch.CollectDrivers(); derr != nil {
			slog.Warn("TIER-3 full-inventory: driver enumerate failed (continuing without drivers)", "error", derr.Error(), "trigger", trigger)
		} else {
			rep.Drivers = make([]*pb.DriverInventory, 0, len(drivers))
			for _, d := range drivers {
				rep.Drivers = append(rep.Drivers, &pb.DriverInventory{
					DeviceName:    d.DeviceName,
					DeviceClass:   d.DeviceClass,
					DriverVersion: d.DriverVersion,
					DriverDate:    d.DriverDate,
					ProviderName:  d.ProviderName,
					HardwareId:    d.HardwareID,
					InfName:       d.InfName,
					Manufacturer:  d.Manufacturer,
					IsSigned:      d.IsSigned,
				})
			}
		}
		// MSIX/AppX/Store inventory — the modern-app surface the Uninstall hive
		// misses. Best-effort; a failure never drops the HW/apps/drivers snapshot.
		if appx, aerr := appxinventory.Collect(); aerr != nil {
			slog.Warn("TIER-3 full-inventory: appx enumerate failed (continuing without appx)", "error", aerr.Error(), "trigger", trigger)
		} else {
			rep.InstalledAppx = make([]*pb.InstalledAppx, 0, len(appx))
			for _, a := range appx {
				rep.InstalledAppx = append(rep.InstalledAppx, &pb.InstalledAppx{
					DisplayName:       a.DisplayName,
					PackageFullName:   a.PackageFullName,
					PackageFamilyName: a.PackageFamilyName,
					Name:              a.Name,
					Version:           a.Version,
					Architecture:      a.Architecture,
					PublisherId:       a.PublisherID,
					InstallLocation:   a.InstallLocation,
					Origin:            a.Origin,
				})
			}
		}
		// Send-on-change gate (BigFix-style drift detection):
		//   - startup / reconnect   -> ALWAYS send (server needs fresh state)
		//   - scheduled & unchanged -> skip until SafetyNetInterval elapsed
		//   - scheduled & changed   -> send immediately (drift)
		fp := fullInventoryFingerprint(rep)
		mu.Lock()
		sameFP := fp == lastFingerprint
		age := time.Since(lastSentAt)
		mu.Unlock()
		if !forceSend && sameFP && age < cfg.FullInventorySafetyNetInterval {
			// Debug, not Info: fires every scheduled cycle when nothing changed;
			// an Info line here is per-cycle log noise.
			slog.Debug("TIER-3 full-inventory: unchanged -> skipping send (safety-net not yet due)",
				"trigger", trigger,
				"age", age.Round(time.Second).String(),
				"safety_net", cfg.FullInventorySafetyNetInterval.String())
			return
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
		reason := "forced"
		switch {
		case !sameFP && lastFingerprint == "":
			reason = "first-send"
		case !sameFP:
			reason = "drift"
		case age >= cfg.FullInventorySafetyNetInterval:
			reason = "safety-net"
		}
		slog.Info("TIER-3 full-inventory: snapshot buffered",
			"trigger", trigger,
			"reason", reason,
			"apps", len(rep.InstalledApps),
			"drivers", len(rep.Drivers),
			"appx", len(rep.InstalledAppx),
			"disks", len(rep.GetHardware().GetDisks()),
			"nics", len(rep.GetHardware().GetNics()),
			"cpu_model", rep.GetHardware().GetCpuModel(),
			"ram_bytes", rep.GetHardware().GetMemoryTotalBytes(),
			"took_ms", time.Since(started).Milliseconds())
		mu.Lock()
		lastFingerprint = fp
		lastSentAt = time.Now()
		mu.Unlock()
		signalDrain(drainSignal)
	}

	// First snapshot at startup — covers the case where the agent is
	// freshly installed or has been down for a long time. forceSend=true
	// bypasses the send-on-change gate.
	safeRun("tier3-full-inventory-startup", func() { snapshot("startup", true) })

	ticker := time.NewTicker(cfg.FullInventoryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			safeRun("tier3-full-inventory-scheduled", func() { snapshot("scheduled", false) })

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
			safeRun("tier3-full-inventory-reconnect", func() { snapshot("reconnect", true) })
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
