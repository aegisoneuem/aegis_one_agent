//go:build windows

package patch

import (
	"fmt"
	"log/slog"
	"strings"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// CollectInstalledKBs returns the COMPLETE set of installed update KBs, as the
// UNION of three local sources so nothing is missed (Vijay's hard requirement —
// appwiz.cpl parity, no KB dropped):
//
//  1. WUA IsInstalled=1 (Online=false) — the authoritative "View installed
//     updates" / Settings list, INCLUDING cumulative updates with their real KB
//     numbers. Local datastore only; no cab, no internet.
//  2. Win32_QuickFixEngineering (QFE) — the Get-Hotfix view; catches a few
//     updates WUA's datastore may not surface.
//  3. CBS Package_for_KB registry (standalone) — belt-and-suspenders.
//
// CBS alone (the previous behavior) missed cumulative updates entirely because
// they are named Package_for_RollupFix~...~<build> (build, not KB). WUA supplies
// those with KB numbers. The result is "KB<digits>" form, deduped + sorted —
// directly readable by the server.
//
// Fail-loud: returns an error only if ALL sources fail (unknown installed set
// is not "nothing installed"). If some succeed, their union is returned and the
// failures are logged.
func CollectInstalledKBs() ([]string, error) {
	var all [][]string
	var errs []string

	if wua, err := InstalledUpdatesWUA(); err != nil {
		errs = append(errs, "wua: "+err.Error())
	} else {
		all = append(all, wua)
	}
	if qfe, err := enumerateInstalledQFE(); err != nil {
		errs = append(errs, "qfe: "+err.Error())
	} else {
		all = append(all, qfe)
	}
	if cbs, err := installedKBsFromCBS(); err != nil {
		errs = append(errs, "cbs: "+err.Error())
	} else {
		all = append(all, cbs)
	}

	union := unionKBs(all...)
	if len(union) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("all installed-KB sources failed: %s", strings.Join(errs, "; "))
	}
	if len(errs) > 0 {
		slog.Warn("installed-KB collection: some sources failed (union of the rest used)",
			"errors", strings.Join(errs, "; "), "count", len(union))
	}
	slog.Debug("installed-KB set collected (WUA ∪ QFE ∪ CBS)", "count", len(union))
	return union, nil
}

// InstalledUpdatesWUA returns installed-update KBs as the Windows Update Agent
// sees them (IsInstalled=1), matching appwiz.cpl "View installed updates".
// Online=false → the LOCAL datastore only: no cab, no network. This is the one
// source that reports cumulative updates with their KB numbers.
func InstalledUpdatesWUA() ([]string, error) {
	cleanup, err := initCOM()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	sessRaw, err := oleutil.CreateObject("Microsoft.Update.Session")
	if err != nil {
		return nil, fmt.Errorf("create Update.Session: %w", err)
	}
	defer sessRaw.Release()
	session, err := sessRaw.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return nil, err
	}
	defer session.Release()

	searcherRaw, err := oleutil.CallMethod(session, "CreateUpdateSearcher")
	if err != nil {
		return nil, err
	}
	searcher := searcherRaw.ToIDispatch()
	defer searcher.Release()

	// HARD no-internet: Online=false makes Search read ONLY the local update
	// datastore. The installed history is local, so this is complete offline and
	// never contacts Windows Update / WSUS.
	if _, err := oleutil.PutProperty(searcher, "Online", false); err != nil {
		return nil, fmt.Errorf("set Online=false: %w", err)
	}

	resRaw, err := oleutil.CallMethod(searcher, "Search", "IsInstalled=1")
	if err != nil {
		if oleErr, ok := err.(*ole.OleError); ok {
			return nil, fmt.Errorf("IsInstalled=1 search failed: code=0x%X %s", oleErr.Code(), oleErr.Description())
		}
		return nil, fmt.Errorf("IsInstalled=1 search failed: %w", err)
	}
	result := resRaw.ToIDispatch()
	defer result.Release()

	updatesRaw, err := oleutil.GetProperty(result, "Updates")
	if err != nil {
		return nil, err
	}
	updates := updatesRaw.ToIDispatch()
	defer updates.Release()
	countRaw, err := oleutil.GetProperty(updates, "Count")
	if err != nil {
		return nil, err
	}
	count := int(countRaw.Val)

	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		itemRaw, err := oleutil.GetProperty(updates, "Item", i)
		if err != nil {
			continue
		}
		item := itemRaw.ToIDispatch()
		out = append(out, stringCollection(item, "KBArticleIDs")...)
		item.Release()
	}
	return out, nil
}

// enumerateInstalledQFE returns every HotFixID from Win32_QuickFixEngineering
// (the Get-Hotfix / wmic qfe view). WMI over COM, no PowerShell, no network.
func enumerateInstalledQFE() ([]string, error) {
	cleanup, err := initCOM()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	locRaw, err := oleutil.CreateObject("WbemScripting.SWbemLocator")
	if err != nil {
		return nil, fmt.Errorf("create SWbemLocator: %w", err)
	}
	defer locRaw.Release()
	loc, err := locRaw.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return nil, err
	}
	defer loc.Release()

	svcRaw, err := oleutil.CallMethod(loc, "ConnectServer", ".", `root\cimv2`)
	if err != nil {
		return nil, fmt.Errorf("WMI ConnectServer: %w", err)
	}
	svc := svcRaw.ToIDispatch()
	defer svc.Release()

	resRaw, err := oleutil.CallMethod(svc, "ExecQuery", "SELECT HotFixID FROM Win32_QuickFixEngineering")
	if err != nil {
		return nil, fmt.Errorf("WMI ExecQuery: %w", err)
	}
	result := resRaw.ToIDispatch()
	defer result.Release()

	countRaw, err := oleutil.GetProperty(result, "Count")
	if err != nil {
		return nil, fmt.Errorf("read Count: %w", err)
	}
	count := int(countRaw.Val)

	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		itemRaw, err := oleutil.CallMethod(result, "ItemIndex", i)
		if err != nil {
			continue
		}
		item := itemRaw.ToIDispatch()
		if id := wmiStr(item, "HotFixID"); id != "" {
			out = append(out, id)
		}
		item.Release()
	}
	return out, nil
}
