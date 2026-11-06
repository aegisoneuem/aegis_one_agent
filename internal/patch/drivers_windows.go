//go:build windows

package patch

import (
	"fmt"
	"log/slog"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// CollectDrivers enumerates installed, signed device drivers via WMI's
// Win32_PnPSignedDriver (the same source Device Manager and BigFix read) and
// returns the rows worth reporting for OEM driver patching. No PowerShell —
// WMI over COM, the same proven path as QFEInstalled and the WUA scan. No
// network. Rows without a comparable version (or without any catalog anchor)
// are dropped as noise by keepDriver.
//
// A collection failure returns an error the caller logs; it never fabricates
// an empty "no drivers" success, because an unknown driver set is not the same
// as "nothing installed" (fail-loud).
func CollectDrivers() ([]DriverInfo, error) {
	cleanup, err := initCOM()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	locRaw, err := oleutil.CreateObject("WbemScripting.SWbemLocator")
	if err != nil {
		return nil, fmt.Errorf("drivers: create SWbemLocator: %w", err)
	}
	defer locRaw.Release()
	loc, err := locRaw.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return nil, fmt.Errorf("drivers: query SWbemLocator: %w", err)
	}
	defer loc.Release()

	svcRaw, err := oleutil.CallMethod(loc, "ConnectServer", ".", `root\cimv2`)
	if err != nil {
		return nil, fmt.Errorf("drivers: WMI ConnectServer: %w", err)
	}
	svc := svcRaw.ToIDispatch()
	defer svc.Release()

	// Static column list (no user input → no query-injection surface). DriverDate
	// comes back as CIM_DATETIME and is normalized below.
	resRaw, err := oleutil.CallMethod(svc, "ExecQuery",
		"SELECT DeviceName, DeviceClass, DriverVersion, DriverDate, DriverProviderName, HardWareID, InfName, Manufacturer, IsSigned FROM Win32_PnPSignedDriver")
	if err != nil {
		return nil, fmt.Errorf("drivers: WMI ExecQuery: %w", err)
	}
	result := resRaw.ToIDispatch()
	defer result.Release()

	countRaw, err := oleutil.GetProperty(result, "Count")
	if err != nil {
		return nil, fmt.Errorf("drivers: read result Count: %w", err)
	}
	count := int(countRaw.Val)

	out := make([]DriverInfo, 0, count)
	for i := 0; i < count; i++ {
		itemRaw, err := oleutil.CallMethod(result, "ItemIndex", i)
		if err != nil {
			slog.Debug("drivers: could not read row, skipping", "index", i, "of", count, "error", err.Error())
			continue
		}
		item := itemRaw.ToIDispatch()
		d := DriverInfo{
			DeviceName:    wmiStr(item, "DeviceName"),
			DeviceClass:   wmiStr(item, "DeviceClass"),
			DriverVersion: wmiStr(item, "DriverVersion"),
			DriverDate:    normalizeCIMDate(wmiStr(item, "DriverDate")),
			ProviderName:  wmiStr(item, "DriverProviderName"),
			HardwareID:    wmiStr(item, "HardWareID"),
			InfName:       wmiStr(item, "InfName"),
			Manufacturer:  wmiStr(item, "Manufacturer"),
			IsSigned:      wmiBool(item, "IsSigned"),
		}
		item.Release()
		if keepDriver(d) {
			out = append(out, d)
		}
	}
	sortDrivers(out)
	// Debug, not Info: this runs on every Tier-3 fingerprint cycle (even when
	// the snapshot is unchanged and skipped), so an Info line here is per-cycle
	// noise. The actual send is logged by the caller.
	slog.Debug("driver inventory collected", "total_rows", count, "reported", len(out))
	return out, nil
}

// wmiStr reads a string property, returning "" for a missing or NULL value
// (many PnP rows leave provider/date/manufacturer empty). go-ole surfaces a
// VT_NULL as an error or an empty VARIANT depending on the property, so any
// failure is treated as "".
func wmiStr(item *ole.IDispatch, prop string) string {
	v, err := oleutil.GetProperty(item, prop)
	if err != nil {
		return ""
	}
	defer v.Clear()
	if v.VT == ole.VT_NULL || v.VT == ole.VT_EMPTY {
		return ""
	}
	return v.ToString()
}

// wmiBool reads a boolean property, returning false for missing/NULL.
func wmiBool(item *ole.IDispatch, prop string) bool {
	v, err := oleutil.GetProperty(item, prop)
	if err != nil {
		return false
	}
	defer v.Clear()
	if v.VT != ole.VT_BOOL {
		return false
	}
	return v.Val != 0
}
