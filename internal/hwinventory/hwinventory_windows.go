//go:build windows

package hwinventory

import (
	"fmt"
	"net"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Collect reads the HW snapshot in a few milliseconds. Any sub-collection
// failure (e.g. a locked registry subkey) is swallowed and its field is
// left zero/empty — a single bad field must not deny the server its full
// snapshot. Only a totally failed call (impossible on Windows API) would
// return an error.
func Collect() (*Inventory, error) {
	inv := &Inventory{}
	inv.CPUVendor, inv.CPUModel = cpuFromRegistry()
	inv.CPULogicalCores = int32(runtime.NumCPU())
	inv.CPUPhysicalCores = physicalCoreCount(inv.CPULogicalCores)
	inv.MemoryTotalBytes = memoryTotal()
	inv.Disks = disks()
	inv.NICs = nics()
	collectBIOSAndChassis(inv)
	return inv, nil
}

// cpuFromRegistry reads the brand/vendor from the standard registry path
// Windows populates at boot. No CPUID assembly required.
func cpuFromRegistry() (vendor, model string) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err != nil {
		return "", ""
	}
	defer k.Close()
	if v, _, err := k.GetStringValue("VendorIdentifier"); err == nil {
		vendor = strings.TrimSpace(v)
	}
	if m, _, err := k.GetStringValue("ProcessorNameString"); err == nil {
		model = strings.TrimSpace(m)
	}
	return
}

// physicalCoreCount uses GetLogicalProcessorInformation to count physical
// cores (relationship = ProcessorCore). On any failure, returns 0 — the
// server treats 0 as unknown, logical count is still reported.
func physicalCoreCount(logical int32) int32 {
	modkernel32 := windows.NewLazySystemDLL("kernel32.dll")
	proc := modkernel32.NewProc("GetLogicalProcessorInformation")
	// Query required buffer size.
	var retLen uint32
	proc.Call(0, uintptr(unsafe.Pointer(&retLen)))
	if retLen == 0 {
		return 0
	}
	const entrySize = 32 // sizeof SYSTEM_LOGICAL_PROCESSOR_INFORMATION on amd64 (ULONG_PTR + Relationship + union); we only read Relationship so this is defensive.
	count := retLen / entrySize
	if count == 0 {
		return 0
	}
	// Allocate buffer and call again.
	buf := make([]byte, retLen)
	r1, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&retLen)))
	if r1 == 0 {
		return 0
	}
	// Each entry: ULONG_PTR ProcessorMask (8) + LOGICAL_PROCESSOR_RELATIONSHIP Relationship (4) + 20 bytes union = 32 bytes on amd64.
	var cores int32
	for i := uint32(0); i < count; i++ {
		off := int(i) * entrySize
		if off+12 > len(buf) {
			break
		}
		relationship := uint32(buf[off+8]) | uint32(buf[off+9])<<8 | uint32(buf[off+10])<<16 | uint32(buf[off+11])<<24
		if relationship == 0 { // RelationProcessorCore
			cores++
		}
	}
	if cores == 0 {
		return 0
	}
	return cores
}

// memoryTotal reads physical RAM via GlobalMemoryStatusEx.
func memoryTotal() int64 {
	type memoryStatusEx struct {
		Length               uint32
		MemoryLoad           uint32
		TotalPhys            uint64
		AvailPhys            uint64
		TotalPageFile        uint64
		AvailPageFile        uint64
		TotalVirtual         uint64
		AvailVirtual         uint64
		AvailExtendedVirtual uint64
	}
	modkernel32 := windows.NewLazySystemDLL("kernel32.dll")
	proc := modkernel32.NewProc("GlobalMemoryStatusEx")
	var ms memoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	r1, _, _ := proc.Call(uintptr(unsafe.Pointer(&ms)))
	if r1 == 0 {
		return 0
	}
	return int64(ms.TotalPhys)
}

// disks enumerates fixed drives (type 3 DRIVE_FIXED) via
// GetLogicalDriveStringsW + GetDriveTypeW + GetDiskFreeSpaceExW +
// GetVolumeInformationW. Removable / network / RAM-disk drives are
// excluded (noisy, change often).
func disks() []Disk {
	modkernel32 := windows.NewLazySystemDLL("kernel32.dll")
	getDrives := modkernel32.NewProc("GetLogicalDriveStringsW")
	getType := modkernel32.NewProc("GetDriveTypeW")
	getFree := modkernel32.NewProc("GetDiskFreeSpaceExW")
	getVolume := modkernel32.NewProc("GetVolumeInformationW")

	buf := make([]uint16, 256)
	r1, _, _ := getDrives.Call(uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
	if r1 == 0 {
		return nil
	}
	var out []Disk
	// Buffer: multiple null-terminated strings "C:\\\0D:\\\0...\0\0".
	start := 0
	for i := 0; i < len(buf); i++ {
		if buf[i] != 0 {
			continue
		}
		if start == i {
			break
		}
		drive := syscall.UTF16ToString(buf[start:i])
		start = i + 1
		if drive == "" {
			continue
		}
		pDrive, _ := syscall.UTF16PtrFromString(drive)
		driveType, _, _ := getType.Call(uintptr(unsafe.Pointer(pDrive)))
		if driveType != 3 { // DRIVE_FIXED
			continue
		}
		d := Disk{MountPoint: drive}
		var freeAvail, totalBytes, totalFree uint64
		if r, _, _ := getFree.Call(uintptr(unsafe.Pointer(pDrive)),
			uintptr(unsafe.Pointer(&freeAvail)),
			uintptr(unsafe.Pointer(&totalBytes)),
			uintptr(unsafe.Pointer(&totalFree))); r != 0 {
			d.TotalBytes = int64(totalBytes)
			d.FreeBytes = int64(totalFree)
		}
		labelBuf := make([]uint16, 256)
		fsBuf := make([]uint16, 256)
		var sn, maxComp, flags uint32
		getVolume.Call(uintptr(unsafe.Pointer(pDrive)),
			uintptr(unsafe.Pointer(&labelBuf[0])), uintptr(len(labelBuf)),
			uintptr(unsafe.Pointer(&sn)),
			uintptr(unsafe.Pointer(&maxComp)),
			uintptr(unsafe.Pointer(&flags)),
			uintptr(unsafe.Pointer(&fsBuf[0])), uintptr(len(fsBuf)))
		d.VolumeLabel = syscall.UTF16ToString(labelBuf)
		d.FileSystem = syscall.UTF16ToString(fsBuf)
		out = append(out, d)
	}
	return out
}

// nics enumerates physical adapters (loopback / tunnel / software adapters
// excluded) via Go stdlib net.Interfaces(). Phase 1 reports name, MAC and
// IP addresses; link speed and up/down status are left zero/unknown
// (would require GetIfEntry2 — tracked as a Phase-1b polish item).
func nics() []NIC {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []NIC
	for _, iface := range ifaces {
		// Skip loopback and interfaces with no HW address.
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(iface.HardwareAddr) == 0 {
			continue
		}
		n := NIC{
			Name:       iface.Name,
			MACAddress: iface.HardwareAddr.String(),
			Status:     "unknown",
		}
		if iface.Flags&net.FlagUp != 0 {
			n.Status = "up"
		} else {
			n.Status = "down"
		}
		addrs, err := iface.Addrs()
		if err == nil {
			for _, a := range addrs {
				ipnet, ok := a.(*net.IPNet)
				if !ok {
					continue
				}
				if v4 := ipnet.IP.To4(); v4 != nil {
					n.IPv4Addresses = append(n.IPv4Addresses, v4.String())
				} else {
					n.IPv6Addresses = append(n.IPv6Addresses, ipnet.IP.String())
				}
			}
		}
		out = append(out, n)
	}
	return out
}

// collectBIOSAndChassis reads SMBIOS-equivalent values from the registry
// path Windows exposes at HKLM\HARDWARE\DESCRIPTION\System\BIOS. This is
// the same data the SMBIOS tables carry (type 0 BIOS, type 1 System) and
// avoids the SMBIOS raw-table parsing + privilege requirements. If the
// registry path cannot be opened, inv.BIOS* / inv.System* stay empty.
func collectBIOSAndChassis(inv *Inventory) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\BIOS`, registry.QUERY_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	readStr := func(name string) string {
		if v, _, err := k.GetStringValue(name); err == nil {
			return strings.TrimSpace(v)
		}
		return ""
	}
	inv.BIOSVendor = readStr("BIOSVendor")
	inv.BIOSVersion = readStr("BIOSVersion")
	inv.BIOSReleaseDate = readStr("BIOSReleaseDate")
	inv.SystemVendor = readStr("SystemManufacturer")
	inv.SystemProduct = readStr("SystemProductName")
	inv.SystemSerialNumber = readStr("SystemSKU") // SystemSKU is a stable identifier; serial-number proper lives under HKLM\HARDWARE\DEVICEMAP (locked-down).
	// Chassis type: integer code; map to a human string for the console.
	inv.ChassisType = chassisFromRegistry()
	// Fall back to runtime.GOOS-based hint when registry is empty (e.g. VM).
	if inv.ChassisType == "" {
		inv.ChassisType = fmt.Sprintf("Unknown (%s/%s)", runtime.GOOS, runtime.GOARCH)
	}
}

// chassisFromRegistry reads SystemFamily or chassis info from a secondary
// registry path. Many OEMs populate "SystemFamily" (e.g. "Latitude",
// "ThinkPad", "MacBook (via Boot Camp)") which is more useful to a
// console than the raw SMBIOS numeric chassis_type.
func chassisFromRegistry() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\BIOS`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	if v, _, err := k.GetStringValue("SystemFamily"); err == nil && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return ""
}
