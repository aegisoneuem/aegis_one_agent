//go:build windows

package main

import (
	"log/slog"
	"runtime"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// Antivirus detection via Windows Security Center (WMI root\SecurityCenter2 →
// AntiVirusProduct). Native, no PowerShell. Lets the console show the right AV
// exclusion guidance for whatever AV the endpoint runs — third-party AV can't be
// auto-excluded, so we at least auto-DETECT it and surface the correct list.
//
// productState is a bitmask; the widely-used decode: bit 0x1000 = real-time protection
// ON, bit 0x10 (low byte) = signatures OUT OF DATE.

const securityCenterNamespace = `root\SecurityCenter2`

// collectAntivirusProducts returns the registered AV products, or nil on Server SKUs /
// machines with no Security Center.
func collectAntivirusProducts() []*pb.AntivirusProduct {
	defer recoverLog("av-detect")
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitialize(0); err != nil {
		if oe, ok := err.(*ole.OleError); !ok || oe.Code() != 1 {
			return nil
		}
	}
	defer ole.CoUninitialize()

	unknown, err := oleutil.CreateObject("WbemScripting.SWbemLocator")
	if err != nil {
		return nil
	}
	defer unknown.Release()
	locator, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return nil
	}
	defer locator.Release()

	servicesV, err := oleutil.CallMethod(locator, "ConnectServer", ".", securityCenterNamespace)
	if err != nil {
		slog.Debug("av-detect: no Security Center (Server SKU?)", "error", err.Error())
		return nil
	}
	services := servicesV.ToIDispatch()
	defer services.Release()

	resultV, err := oleutil.CallMethod(services, "ExecQuery", "SELECT displayName, productState FROM AntiVirusProduct")
	if err != nil {
		return nil
	}
	result := resultV.ToIDispatch()
	defer result.Release()

	countV, err := oleutil.GetProperty(result, "Count")
	if err != nil {
		return nil
	}
	count := int(countV.Val)

	out := make([]*pb.AntivirusProduct, 0, count)
	for i := 0; i < count; i++ {
		itemV, err := oleutil.CallMethod(result, "ItemIndex", i)
		if err != nil {
			continue
		}
		item := itemV.ToIDispatch()
		name := oleProp(item, "displayName")
		state := uint32(olePropInt(item, "productState"))
		item.Release()
		if name == "" {
			continue
		}
		out = append(out, &pb.AntivirusProduct{
			Name:         name,
			Enabled:      state&0x1000 != 0, // real-time protection on
			UpToDate:     state&0x10 == 0,   // low byte 0x10 = out of date
			ProductState: state,
		})
	}
	return out
}

func oleProp(d *ole.IDispatch, name string) string {
	v, err := oleutil.GetProperty(d, name)
	if err != nil {
		return ""
	}
	defer v.Clear()
	return v.ToString()
}

func olePropInt(d *ole.IDispatch, name string) int64 {
	v, err := oleutil.GetProperty(d, name)
	if err != nil {
		return 0
	}
	defer v.Clear()
	return v.Val
}
