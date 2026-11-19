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
	inv.CPUMHz = cpuMHzFromRegistry()
	if inv.CPUPhysicalCores > 0 {
		inv.CPUThreadsPerCore = inv.CPULogicalCores / inv.CPUPhysicalCores
	}
	inv.CPUArchitecture = cpuArchitecture()
	inv.MemoryTotalBytes = memoryTotal()
	inv.Disks = disks()
	inv.NICs = nics()
	collectBIOSAndChassis(inv)
	collectOSDetails(inv)
	inv.SystemUUID = systemUUIDFromRegistry()
	inv.Virtualization = detectVirtualization(inv)
	return inv, nil
}

// cpuMHzFromRegistry returns the rated CPU clock speed (~MHz value Windows
// writes at boot). 0 when the registry is locked. This is the rated speed,
// not current frequency.
func cpuMHzFromRegistry() int32 {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err != nil {
		return 0
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("~MHz")
	if err != nil {
		return 0
	}
	return int32(v)
}

// cpuArchitecture returns the OS build target architecture. amd64 covers
// most Windows endpoints today; arm64 is for Windows-on-ARM; x86 is 32-bit.
// On the agent build this is runtime.GOARCH — reliable, no syscall needed.
func cpuArchitecture() string {
	switch runtime.GOARCH {
	case "amd64":
		return "amd64"
	case "arm64":
		return "arm64"
	case "386":
		return "x86"
	default:
		return runtime.GOARCH
	}
}

// collectOSDetails reads the richer OS identity (edition, display version,
// authoritative build, service pack) from the standard CurrentVersion
// registry path. These are what Settings → System → About shows the user
// and what a server-side catalog match keys off.
func collectOSDetails(inv *Inventory) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
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
	readInt := func(name string) uint32 {
		if v, _, err := k.GetIntegerValue(name); err == nil {
			return uint32(v)
		}
		return 0
	}
	inv.OSProductName = readStr("ProductName")
	inv.OSDisplayVersion = readStr("DisplayVersion")
	if inv.OSDisplayVersion == "" {
		inv.OSDisplayVersion = readStr("ReleaseId")
	}
	// Authoritative cumulative build = CurrentBuild.UBR (UBR = Update Build Revision).
	build := readStr("CurrentBuild")
	ubr := readInt("UBR")
	if build != "" {
		if ubr > 0 {
			inv.OSBuild = fmt.Sprintf("%s.%d", build, ubr)
		} else {
			inv.OSBuild = build
		}
	}
	// Service pack: modern Windows (10+) has no SP; this stays empty.
	// Older Windows uses "CSDVersion" (e.g. "Service Pack 1").
	inv.OSServicePack = readStr("CSDVersion")
}

// systemUUIDFromRegistry reads the SMBIOS System UUID that Windows exposes
// in the BIOS registry subkey (same value SMBIOS type 1 publishes; the
// enroll dedup path reads the SMBIOS table directly, but the registry
// mirror is cheaper for the inventory snapshot).
func systemUUIDFromRegistry() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\BIOS`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	// The BIOS key exposes "SystemSKU" and often "BaseBoardManufacturer" but
	// NOT a plain system UUID. On some systems "ComputerSystemProductUUID"
	// appears; we try common names and fall back to empty.
	for _, name := range []string{"SystemUUID", "ComputerSystemProductUUID", "SystemFamilyUUID"} {
		if v, _, err := k.GetStringValue(name); err == nil && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// detectVirtualization identifies common hypervisors from the BIOS /
// system vendor strings. Not a full VMM detection (would need CPUID leaf
// 0x40000000 reads or raw SMBIOS parsing), but catches the overwhelming
// majority of VM endpoints. Values match a short closed vocabulary so a
// server-side filter can key off exact strings.
func detectVirtualization(inv *Inventory) string {
	hay := strings.ToLower(inv.SystemVendor + " " + inv.SystemProduct + " " + inv.BIOSVendor + " " + inv.BIOSVersion)
	switch {
	case strings.Contains(hay, "vmware"):
		return "vmware"
	case strings.Contains(hay, "virtualbox") || strings.Contains(hay, "innotek"):
		return "virtualbox"
	case strings.Contains(hay, "microsoft") && (strings.Contains(hay, "virtual") || strings.Contains(hay, "hyper-v")):
		return "hyperv"
	case strings.Contains(hay, "xen"):
		return "xen"
	case strings.Contains(hay, "kvm"):
		return "kvm"
	case strings.Contains(hay, "qemu") || strings.Contains(hay, "bochs"):
		return "qemu"
	case strings.Contains(hay, "parallels"):
		return "parallels"
	case strings.Contains(hay, "amazon ec2") || strings.Contains(hay, "amazon web services"):
		return "aws"
	case strings.Contains(hay, "google compute engine") || strings.Contains(hay, "google ce"):
		return "gcp"
	default:
		if inv.SystemVendor == "" && inv.BIOSVendor == "" {
			return "unknown"
		}
		return "none"
	}
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
// cores (relatiosvship = ProcessorCore). On any failure, returns 0 — the
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
	const entrySize = 32 // sizeof SYSTEM_LOGICAL_PROCESSOR_INFORMATION on amd64 (ULONG_PTR + Relatiosvship + union); we only read Relatiosvship so this is defensive.
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
	// Each entry: ULONG_PTR ProcessorMask (8) + LOGICAL_PROCESSOR_RELATIOSVSHIP Relatiosvship (4) + 20 bytes union = 32 bytes on amd64.
	var cores int32
	for i := uint32(0); i < count; i++ {
		off := int(i) * entrySize
		if off+12 > len(buf) {
			break
		}
		relatiosvship := uint32(buf[off+8]) | uint32(buf[off+9])<<8 | uint32(buf[off+10])<<16 | uint32(buf[off+11])<<24
		if relatiosvship == 0 { // RelationProcessorCore
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
		d := Disk{MountPoint: drive, DriveType: detectDriveMedia(drive)}
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

// detectDriveMedia asks the storage stack whether a drive has rotational
// media (HDD) or not (SSD / NVMe). Uses IOCTL_STORAGE_QUERY_PROPERTY with
// StorageDeviceSeekPenaltyProperty (NoSeekPenalty = true → SSD-class),
// plus StorageAdapterProperty bus-type probe to tell NVMe apart. On any
// failure returns "Unknown" (never fails the whole snapshot).
//
// drive is the root ("C:\\"); we open \\.\C: as the volume handle.
func detectDriveMedia(drive string) string {
	if len(drive) < 2 || drive[1] != ':' {
		return "Unknown"
	}
	devicePath := `\\.\` + strings.TrimRight(drive, `\`)
	pDev, err := syscall.UTF16PtrFromString(devicePath)
	if err != nil {
		return "Unknown"
	}
	// Open with 0 access (metadata only — no admin privileges required).
	h, err := windows.CreateFile(pDev,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0)
	if err != nil {
		return "Unknown"
	}
	defer windows.CloseHandle(h)

	// IOCTL_STORAGE_QUERY_PROPERTY + StorageDeviceSeekPenaltyProperty.
	const (
		ioctlStorageQueryProperty           = 0x002D1400
		storageDeviceSeekPenaltyProperty    = 7
		storageAdapterProperty              = 1
		propertyStandardQuery               = 0
		busTypeNvme                         = 0x11
	)
	type storagePropertyQuery struct {
		PropertyID uint32
		QueryType  uint32
		Additional [1]byte
	}
	type deviceSeekPenaltyDescriptor struct {
		Version           uint32
		Size              uint32
		IncursSeekPenalty uint32 // actually a BOOLEAN; 4-byte aligned
	}
	type storageAdapterDescriptor struct {
		Version              uint32
		Size                 uint32
		MaximumTransferLen   uint32
		MaximumPhysicalPages uint32
		AlignmentMask        uint32
		AdapterUsesPIO       byte
		AdapterScansDown     byte
		CommandQueueing      byte
		AcceleratedTransfer  byte
		BusType              byte
		_pad1                [3]byte
		BusMajorVersion      uint16
		BusMinorVersion      uint16
		SrbType              byte
		AddressType          byte
	}
	// Seek-penalty query.
	q := storagePropertyQuery{PropertyID: storageDeviceSeekPenaltyProperty, QueryType: propertyStandardQuery}
	var sp deviceSeekPenaltyDescriptor
	var bytesReturned uint32
	err = windows.DeviceIoControl(h,
		ioctlStorageQueryProperty,
		(*byte)(unsafe.Pointer(&q)), uint32(unsafe.Sizeof(q)),
		(*byte)(unsafe.Pointer(&sp)), uint32(unsafe.Sizeof(sp)),
		&bytesReturned, nil)
	// Adapter query (optional — only to distinguish NVMe from SATA SSD).
	qa := storagePropertyQuery{PropertyID: storageAdapterProperty, QueryType: propertyStandardQuery}
	var ad storageAdapterDescriptor
	var bytesReturnedA uint32
	errA := windows.DeviceIoControl(h,
		ioctlStorageQueryProperty,
		(*byte)(unsafe.Pointer(&qa)), uint32(unsafe.Sizeof(qa)),
		(*byte)(unsafe.Pointer(&ad)), uint32(unsafe.Sizeof(ad)),
		&bytesReturnedA, nil)
	// Classify:
	//  - IncursSeekPenalty = 0 (SSD-class) + BusType NVMe → "NVMe"
	//  - IncursSeekPenalty = 0 → "SSD"
	//  - IncursSeekPenalty = 1 → "HDD"
	//  - Neither → "Unknown"
	if err == nil {
		if sp.IncursSeekPenalty == 0 {
			if errA == nil && ad.BusType == busTypeNvme {
				return "NVMe"
			}
			return "SSD"
		}
		return "HDD"
	}
	// If seek-penalty query failed but adapter says NVMe, call it NVMe anyway.
	if errA == nil && ad.BusType == busTypeNvme {
		return "NVMe"
	}
	return "Unknown"
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
