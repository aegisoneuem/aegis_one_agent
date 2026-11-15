//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Native DEX battery reader — GetSystemPowerStatus (kernel32, one call, no admin).
// Point-in-time state (charge %, AC line, battery present); no WMI, no PowerShell.
var procGetSystemPowerStatus = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemPowerStatus")

// systemPowerStatus mirrors SYSTEM_POWER_STATUS.
type systemPowerStatus struct {
	ACLineStatus        byte   // 0=offline, 1=online, 255=unknown
	BatteryFlag         byte   // bitmask; 128=no system battery, 255=unknown
	BatteryLifePercent  byte   // 0-100, 255=unknown
	SystemStatusFlag    byte   // power-saver on/off (reserved on older OS)
	BatteryLifeTime     uint32 // seconds remaining, 0xFFFFFFFF=unknown
	BatteryFullLifeTime uint32
}

// batterySample is one battery reading. OK is false if the API call failed.
// ChargePct is -1 when the charge is unknown. Present is false on a desktop/VM
// with no battery.
type batterySample struct {
	Present   bool
	ACOnline  bool
	ChargePct int
	OK        bool
}

// readBattery returns the current power state. Present=false (no battery) is a
// valid result on desktops/servers/VMs — not an error.
func readBattery() batterySample {
	var s systemPowerStatus
	if r, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&s))); r == 0 {
		return batterySample{}
	}
	present := s.BatteryFlag&0x80 == 0 && s.BatteryFlag != 0xFF // 0x80 = "no system battery"
	charge := -1
	if present && s.BatteryLifePercent != 0xFF {
		charge = int(s.BatteryLifePercent)
	}
	return batterySample{
		Present:   present,
		ACOnline:  s.ACLineStatus == 1,
		ChargePct: charge,
		OK:        true,
	}
}
