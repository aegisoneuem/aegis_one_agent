package patch

import (
	"sort"
	"strings"
)

// DriverInfo is one installed device driver as reported for OEM/third-party
// driver patching. The agent reports facts only; the SERVER matches
// (HardwareID + ProviderName + current DriverVersion/DriverDate) against its
// vendor driver catalog (Dell/HP/Lenovo/etc.) to decide what is outdated and
// dispatch a signed driver-update deploy. This mirrors the BigFix/SCCM model:
// lean device inventory + central vendor catalog + server-side diff.
//
// Microsoft-delivered drivers already surface in the wsusscn2.cab scan
// (classification "Drivers"); THIS inventory additionally covers OEM drivers
// that Windows Update does not carry — the competitive gap vs relying on WU
// alone.
type DriverInfo struct {
	DeviceName    string // "NVIDIA GeForce RTX 3060"
	DeviceClass   string // "Display", "Net", "System", ...
	DriverVersion string // "31.0.15.3623" (the load-bearing compare key)
	DriverDate    string // normalized "YYYY-MM-DD" (from CIM_DATETIME)
	ProviderName  string // "NVIDIA", "Intel", "Realtek"
	HardwareID    string // "PCI\VEN_10DE&DEV_2504..." — server's catalog match key
	InfName       string // "oem123.inf"
	Manufacturer  string // device manufacturer string
	IsSigned      bool   // Authenticode-signed driver package
}

// normalizeCIMDate converts a WMI CIM_DATETIME string
// ("yyyymmddHHMMSS.ffffff±UUU", e.g. "20230510000000.000000-000") to a plain
// "YYYY-MM-DD". Returns "" when the input is empty or not a parseable date —
// an unknown date must never masquerade as a real one.
func normalizeCIMDate(cim string) string {
	cim = strings.TrimSpace(cim)
	if len(cim) < 8 {
		return ""
	}
	ymd := cim[:8]
	for i := 0; i < 8; i++ {
		if ymd[i] < '0' || ymd[i] > '9' {
			return ""
		}
	}
	// Reject an all-zero date (some drivers report "00000000..."): not a date.
	if ymd == "00000000" {
		return ""
	}
	return ymd[:4] + "-" + ymd[4:6] + "-" + ymd[6:8]
}

// sortDrivers orders drivers deterministically by (HardwareID, DeviceName,
// DriverVersion) so WMI enumeration order never changes the report bytes —
// otherwise an unchanged device could falsely trip inventory "drift" and
// resend every poll. Stable, in place.
func sortDrivers(ds []DriverInfo) {
	sort.SliceStable(ds, func(i, j int) bool {
		if ds[i].HardwareID != ds[j].HardwareID {
			return ds[i].HardwareID < ds[j].HardwareID
		}
		if ds[i].DeviceName != ds[j].DeviceName {
			return ds[i].DeviceName < ds[j].DeviceName
		}
		return ds[i].DriverVersion < ds[j].DriverVersion
	})
}

// keepDriver decides whether a raw Win32_PnPSignedDriver row is worth
// reporting. Patch matching needs a version to compare, so a row with no
// DriverVersion is noise (many PnP stubs have none). A hardware ID OR a
// provider lets the server anchor the catalog match; without either plus a
// version, the row cannot be acted on.
func keepDriver(d DriverInfo) bool {
	if strings.TrimSpace(d.DriverVersion) == "" {
		return false
	}
	if strings.TrimSpace(d.HardwareID) == "" && strings.TrimSpace(d.ProviderName) == "" {
		return false
	}
	return true
}
