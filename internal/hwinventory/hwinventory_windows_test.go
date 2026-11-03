//go:build windows

package hwinventory

import "testing"

// TestCollect_FillsBasicFields proves the Phase-1 collector reads enough
// to be useful on a bare Windows VM: the logical core count is always
// available via runtime, memory total is always available via
// GlobalMemoryStatusEx, and at least one fixed disk exists on any
// bootable Windows install (C:).
func TestCollect_FillsBasicFields(t *testing.T) {
	inv, err := Collect()
	if err != nil {
		t.Fatalf("Collect returned error: %v", err)
	}
	if inv == nil {
		t.Fatal("Collect returned nil inventory")
	}
	if inv.CPULogicalCores <= 0 {
		t.Errorf("CPULogicalCores = %d, want > 0", inv.CPULogicalCores)
	}
	if inv.MemoryTotalBytes <= 0 {
		t.Errorf("MemoryTotalBytes = %d, want > 0", inv.MemoryTotalBytes)
	}
	if len(inv.Disks) == 0 {
		t.Error("Disks list is empty, expected at least one fixed drive (C:)")
	}
	// NICs: at least one physical adapter should exist on a real machine or
	// VM with a NIC. Skip if the test host happens to have none (unlikely).
	if len(inv.NICs) == 0 {
		t.Log("warning: NICs list empty — likely a test host with no physical NICs")
	}
	// CPU model may be empty on a stripped-down VM; don't fail, just log.
	if inv.CPUModel == "" {
		t.Logf("warning: CPUModel empty — HKLM registry may be locked")
	}
	// Physical cores can be 0 (unknown) on some systems; just check sanity.
	if inv.CPUPhysicalCores > inv.CPULogicalCores {
		t.Errorf("CPUPhysicalCores (%d) > CPULogicalCores (%d) is impossible", inv.CPUPhysicalCores, inv.CPULogicalCores)
	}
}

func TestCollect_DiskSanity(t *testing.T) {
	inv, _ := Collect()
	for _, d := range inv.Disks {
		if d.MountPoint == "" {
			t.Error("disk entry with empty MountPoint")
		}
		if d.TotalBytes < 0 || d.FreeBytes < 0 {
			t.Errorf("negative byte count on %s", d.MountPoint)
		}
		if d.FreeBytes > d.TotalBytes && d.TotalBytes > 0 {
			t.Errorf("%s: FreeBytes (%d) > TotalBytes (%d)", d.MountPoint, d.FreeBytes, d.TotalBytes)
		}
	}
}

func TestCollect_NICSanity(t *testing.T) {
	inv, _ := Collect()
	for _, n := range inv.NICs {
		if n.Name == "" {
			t.Error("NIC entry with empty Name")
		}
		if n.MACAddress == "" {
			t.Errorf("NIC %s has empty MAC", n.Name)
		}
		if n.Status != "up" && n.Status != "down" && n.Status != "unknown" {
			t.Errorf("NIC %s has unexpected status %q", n.Name, n.Status)
		}
	}
}
