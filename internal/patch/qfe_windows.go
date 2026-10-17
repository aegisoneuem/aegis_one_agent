//go:build windows

package patch

import (
	"log/slog"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// QFEInstalled reports whether the KB appears in Win32_QuickFixEngineering — the
// legacy hotfix/QFE list that `wmic qfe` and Get-HotFix read. Some updates live
// ONLY there: no Package_for_KB CBS package and not tracked by WUA's installed
// store (e.g. KB5011497 on Server 2022). For those, "WUA can't find it" does NOT
// mean "not installed" — this check lets the agent report the truth: installed,
// but Windows does not allow removing it. Queried via WMI COM (no PowerShell);
// any failure returns false (the caller then reports the WUA view).
func QFEInstalled(kb string) bool {
	kb = normalizeKB(kb)
	if kb == "" {
		return false
	}
	cleanup, err := initCOM()
	if err != nil {
		return false
	}
	defer cleanup()

	locRaw, err := oleutil.CreateObject("WbemScripting.SWbemLocator")
	if err != nil {
		slog.Debug("qfe: create SWbemLocator failed", "error", err.Error())
		return false
	}
	defer locRaw.Release()
	loc, err := locRaw.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return false
	}
	defer loc.Release()

	svcRaw, err := oleutil.CallMethod(loc, "ConnectServer", ".", `root\cimv2`)
	if err != nil {
		slog.Debug("qfe: WMI ConnectServer failed", "error", err.Error())
		return false
	}
	svc := svcRaw.ToIDispatch()
	defer svc.Release()

	// HotFixID values are stored with the KB prefix ("KB5011497"). KB input is
	// normalized to digits, so no quote-injection surface.
	resRaw, err := oleutil.CallMethod(svc, "ExecQuery",
		"SELECT HotFixID FROM Win32_QuickFixEngineering WHERE HotFixID = 'KB"+kb+"'")
	if err != nil {
		slog.Debug("qfe: WMI query failed", "error", err.Error())
		return false
	}
	result := resRaw.ToIDispatch()
	defer result.Release()

	countRaw, err := oleutil.GetProperty(result, "Count")
	if err != nil {
		return false
	}
	return countRaw.Val > 0
}
