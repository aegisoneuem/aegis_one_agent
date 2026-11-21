//go:build linux

package hwinventory

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// archName maps the Go arch to the inventory's human arch label (matches the Windows
// collector's style, e.g. "x64").
func archName() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "arm64":
		return "ARM64"
	case "386":
		return "x86"
	default:
		return runtime.GOARCH
	}
}

// Collect gathers the Linux hardware/OS inventory natively (no shell for the core
// reads): /etc/os-release, /proc/cpuinfo, /proc/meminfo, /sys/class/dmi/id, /proc/mounts
// + statfs, and net.Interfaces. systemd-detect-virt is used (cheaply) for the virt hint.
func Collect() (*Inventory, error) {
	inv := &Inventory{
		CPULogicalCores: int32(runtime.NumCPU()),
		CPUArchitecture: archName(),
	}
	collectOSRelease(inv)
	collectCPU(inv)
	collectMem(inv)
	collectDMI(inv)
	inv.Disks = collectDisks()
	inv.NICs = collectNICs()
	inv.Virtualization = detectVirt()
	return inv, nil
}

func readFirstLineTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
}

// --- OS / kernel ---

func collectOSRelease(inv *Inventory) {
	kv := map[string]string{}
	if f, err := os.Open("/etc/os-release"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if i := strings.IndexByte(line, '='); i > 0 {
				k := line[:i]
				v := strings.Trim(line[i+1:], `"`)
				kv[k] = v
			}
		}
		f.Close()
	}
	inv.OSProductName = kv["PRETTY_NAME"] // e.g. "Ubuntu 24.04.1 LTS"
	if inv.OSProductName == "" {
		inv.OSProductName = kv["NAME"]
	}
	inv.OSDisplayVersion = kv["VERSION_ID"] // e.g. "24.04"
	inv.OSBuild = readFirstLineTrim("/proc/sys/kernel/osrelease")  // kernel, e.g. 6.8.0-45-generic
}

// --- CPU ---

func collectCPU(inv *Inventory) {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return
	}
	defer f.Close()
	coreKeys := map[string]struct{}{}
	var curPhys, curCore string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		k, v, ok := splitKV(line)
		if !ok {
			if line == "" { // end of a processor block
				if curPhys != "" || curCore != "" {
					coreKeys[curPhys+":"+curCore] = struct{}{}
				}
				curPhys, curCore = "", ""
			}
			continue
		}
		switch k {
		case "model name":
			if inv.CPUModel == "" {
				inv.CPUModel = v
			}
		case "vendor_id":
			if inv.CPUVendor == "" {
				inv.CPUVendor = v
			}
		case "cpu MHz":
			if inv.CPUMHz == 0 {
				if mhz, e := strconv.ParseFloat(v, 64); e == nil {
					inv.CPUMHz = int32(mhz)
				}
			}
		case "physical id":
			curPhys = v
		case "core id":
			curCore = v
		}
	}
	if curPhys != "" || curCore != "" {
		coreKeys[curPhys+":"+curCore] = struct{}{}
	}
	if n := len(coreKeys); n > 0 {
		inv.CPUPhysicalCores = int32(n)
	} else {
		inv.CPUPhysicalCores = inv.CPULogicalCores
	}
	if inv.CPUPhysicalCores > 0 {
		inv.CPUThreadsPerCore = inv.CPULogicalCores / inv.CPUPhysicalCores
	}
}

func splitKV(line string) (k, v string, ok bool) {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
}

// --- Memory ---

func collectMem(inv *Inventory) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "MemTotal:") {
			fields := strings.Fields(sc.Text()) // "MemTotal: 16384000 kB"
			if len(fields) >= 2 {
				if kb, e := strconv.ParseInt(fields[1], 10, 64); e == nil {
					inv.MemoryTotalBytes = kb * 1024
				}
			}
			return
		}
	}
}

// --- DMI (SMBIOS) ---

func collectDMI(inv *Inventory) {
	const d = "/sys/class/dmi/id/"
	inv.SystemVendor = readFirstLineTrim(d + "sys_vendor")
	inv.SystemProduct = readFirstLineTrim(d + "product_name")
	inv.SystemSerialNumber = readFirstLineTrim(d + "product_serial")
	inv.SystemUUID = strings.ToUpper(readFirstLineTrim(d + "product_uuid"))
	inv.BIOSVendor = readFirstLineTrim(d + "bios_vendor")
	inv.BIOSVersion = readFirstLineTrim(d + "bios_version")
	inv.BIOSReleaseDate = readFirstLineTrim(d + "bios_date")
	inv.ChassisType = readFirstLineTrim(d + "chassis_type")
}

// --- Disks ---

// localFS is the set of real on-disk filesystems we report (skips tmpfs, squashfs
// snaps, overlay, cgroup, proc, network mounts, etc.).
var localFS = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true,
	"vfat": true, "f2fs": true, "zfs": true, "reiserfs": true, "jfs": true, "ntfs": true,
}

func collectDisks() []Disk {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	seen := map[string]bool{}
	var out []Disk
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text()) // dev mountpoint fstype opts ...
		if len(fields) < 3 {
			continue
		}
		dev, mount, fstype := fields[0], fields[1], fields[2]
		if !localFS[fstype] || seen[mount] {
			continue
		}
		seen[mount] = true
		var st syscall.Statfs_t
		if syscall.Statfs(mount, &st) != nil {
			continue
		}
		bs := int64(st.Bsize)
		out = append(out, Disk{
			MountPoint: mount,
			FileSystem: fstype,
			TotalBytes: int64(st.Blocks) * bs,
			FreeBytes:  int64(st.Bavail) * bs,
			DriveType:  driveType(dev),
		})
	}
	return out
}

func driveType(dev string) string {
	base := dev
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if strings.HasPrefix(base, "nvme") {
		return "NVMe"
	}
	// Strip partition digits to get the parent block device (sda1 -> sda).
	disk := strings.TrimRight(base, "0123456789")
	if r := readFirstLineTrim("/sys/block/" + disk + "/queue/rotational"); r == "0" {
		return "SSD"
	} else if r == "1" {
		return "HDD"
	}
	return "Unknown"
}

// --- NICs ---

func collectNICs() []NIC {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []NIC
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 || ifc.HardwareAddr == nil || len(ifc.HardwareAddr) == 0 {
			continue
		}
		n := NIC{Name: ifc.Name, MACAddress: ifc.HardwareAddr.String()}
		if ifc.Flags&net.FlagUp != 0 {
			n.Status = "up"
		} else {
			n.Status = "down"
		}
		if addrs, e := ifc.Addrs(); e == nil {
			for _, a := range addrs {
				ip, _, _ := net.ParseCIDR(a.String())
				if ip == nil {
					continue
				}
				if ip.To4() != nil {
					n.IPv4Addresses = append(n.IPv4Addresses, ip.String())
				} else {
					n.IPv6Addresses = append(n.IPv6Addresses, ip.String())
				}
			}
		}
		if sp := readFirstLineTrim("/sys/class/net/" + ifc.Name + "/speed"); sp != "" {
			if mbps, e := strconv.ParseInt(sp, 10, 64); e == nil && mbps > 0 {
				n.LinkSpeedBPS = mbps * 1_000_000
			}
		}
		out = append(out, n)
	}
	return out
}

// --- Virtualization ---

func detectVirt() string {
	out, err := exec.Command("systemd-detect-virt").Output()
	if err != nil {
		return "" // "none" exits non-zero on bare metal
	}
	v := strings.TrimSpace(string(out))
	if v == "none" {
		return ""
	}
	return v
}
