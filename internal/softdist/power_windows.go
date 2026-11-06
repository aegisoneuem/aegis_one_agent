//go:build windows

package softdist

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// systemPowerStatus mirrors the Win32 SYSTEM_POWER_STATUS struct.
type systemPowerStatus struct {
	ACLineStatus        byte // 0 = offline (battery), 1 = online (AC), 255 = unknown
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

// SystemOnACPower reports whether the machine is on AC/mains power. The second
// return is false when power state cannot be determined (e.g. a desktop/VM with
// no battery subsystem reports "unknown") — the caller decides how to treat an
// unknown state. Pure GetSystemPowerStatus syscall; no COM, no WMI.
func SystemOnACPower() (onAC bool, known bool) {
	var s systemPowerStatus
	r, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&s)))
	if r == 0 {
		return false, false // call failed
	}
	switch s.ACLineStatus {
	case 0:
		return false, true // on battery
	case 1:
		return true, true // on AC
	default:
		return false, false // 255 = unknown (desktop/VM/no battery)
	}
}

var (
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemPowerStatus = kernel32.NewProc("GetSystemPowerStatus")
)
