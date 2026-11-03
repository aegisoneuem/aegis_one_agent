// Package hwinventory collects the HARDWARE section of a Tier-3 full
// inventory snapshot (CPU, memory, disks, NICs, BIOS, chassis). It is
// Windows-first (hwinventory_windows.go) with a no-op stub on other
// platforms (hwinventory_other.go).
//
// Design constraints (Vijay's rules, 2026-11-03):
//   - Agent binary size <= 25 MB — no WMI, no COM, no cgo. Collector
//     uses Win32 syscalls via golang.org/x/sys/windows + registry reads
//     via golang.org/x/sys/windows/registry (both already linked).
//   - Low CPU / RAM — snapshot takes single-digit ms; no persistent
//     subscriptions, no background threads.
//   - Fields that cannot be read are left zero ("" / 0) rather than
//     failing the whole collection — the server treats zero as unknown.
package hwinventory

// Inventory is the collected HW snapshot. Keep this proto-aligned so the
// agent can map it 1:1 onto pb.HardwareInventory without a translator.
type Inventory struct {
	CPUVendor         string
	CPUModel          string
	CPUPhysicalCores  int32
	CPULogicalCores   int32
	MemoryTotalBytes  int64
	Disks             []Disk
	NICs              []NIC
	BIOSVendor        string
	BIOSVersion       string
	BIOSReleaseDate   string
	SystemVendor      string
	SystemProduct     string
	SystemSerialNumber string
	ChassisType       string
}

type Disk struct {
	MountPoint string
	VolumeLabel string
	FileSystem string
	TotalBytes int64
	FreeBytes  int64
}

type NIC struct {
	Name          string
	MACAddress    string
	IPv4Addresses []string
	IPv6Addresses []string
	LinkSpeedBPS  int64
	Status        string
}
