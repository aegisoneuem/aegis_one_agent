//go:build windows

package patch

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// ScanOffline performs a full applicability scan against a local wsusscn2.cab
// offline catalog instead of live Windows Update — deterministic, network-free,
// and identical across devices regardless of their WU configuration. This is the
// RECONCILIATION safety-net path (occasional / on-demand), not the frequent one.
//
// It registers the cab as a temporary "scan package service", searches against
// only that service (ssOthers + its ServiceID, Online=false), reuses the same
// rich Update extraction as the online scan, then removes the temporary service.
// Ref: learn.microsoft.com/windows/win32/wua_sdk/using-wua-to-scan-for-updates-offline
func ScanOffline(cabPath string) (*OfflineScanResult, error) {
	// AddScanPackageService requires an ABSOLUTE path to an existing cab file;
	// a relative path throws a generic COM exception. Resolve and validate first.
	absCab, err := filepath.Abs(cabPath)
	if err != nil {
		return nil, fmt.Errorf("resolve cab path %q: %w", cabPath, err)
	}
	info, err := os.Stat(absCab)
	if err != nil {
		// HARD no-internet rule: if the local cab is missing/unreadable, report
		// the reconciliation scan as unavailable. NEVER fall back to a live
		// online Windows Update search — the endpoint must never reach the
		// internet for detection. The cab is delivered to the endpoint via
		// peer-cache; a missing cab is a delivery problem, not a reason to go online.
		return nil, fmt.Errorf("reconciliation scan unavailable: cab file not present at %q: %w", absCab, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("cab path %q is a directory, not a file", absCab)
	}
	cabPath = absCab
	slog.Info("offline scan: using catalog", "cab", cabPath, "size_mb", fmt.Sprintf("%.1f", float64(info.Size())/1024/1024))

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

	smRaw, err := oleutil.CreateObject("Microsoft.Update.ServiceManager")
	if err != nil {
		return nil, fmt.Errorf("create ServiceManager: %w", err)
	}
	defer smRaw.Release()
	sm, err := smRaw.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return nil, err
	}
	defer sm.Release()

	// Register the cab as an offline scan package service. Match Microsoft's
	// canonical offline-scan sample exactly: two args (name, path), no flags —
	// passing a flags value registers the service in a mode where the searcher
	// does not actually read the cab (search returns 0 for everything).
	svcRaw, err := oleutil.CallMethod(sm, "AddScanPackageService", "Aegis Offline Sync Service", cabPath)
	if err != nil {
		// This step requires Administrator. In production the agent runs as a
		// LocalSystem service (elevated); a generic COM exception here almost
		// always means the process is not elevated.
		return nil, fmt.Errorf("AddScanPackageService(%q) failed — offline scan requires Administrator (the agent runs as a LocalSystem service in production): %w", cabPath, err)
	}
	svc := svcRaw.ToIDispatch()
	defer svc.Release()

	serviceID := stringProp(svc, "ServiceID")
	if serviceID == "" {
		return nil, fmt.Errorf("offline scan: registered service returned no ServiceID")
	}
	slog.Info("offline scan: registered scan package service", "cab", cabPath, "service_id", serviceID)

	// Remove the transient service when done (best-effort).
	defer func() {
		if _, err := oleutil.CallMethod(sm, "RemoveService", serviceID); err != nil {
			slog.Debug("offline scan: RemoveService failed (harmless for a transient service)", "error", err.Error())
		}
	}()

	searcherRaw, err := oleutil.CallMethod(session, "CreateUpdateSearcher")
	if err != nil {
		return nil, err
	}
	searcher := searcherRaw.ToIDispatch()
	defer searcher.Release()

	// Scope the search to the offline cab service (ssOthers + its ServiceID),
	// matching Microsoft's offline-scan sample. Do NOT set Online — the sample
	// leaves it alone; ServerSelection + ServiceID already make this offline.
	if _, err := oleutil.PutProperty(searcher, "ServerSelection", 3); err != nil { // ssOthers
		return nil, fmt.Errorf("set ServerSelection: %w", err)
	}
	if _, err := oleutil.PutProperty(searcher, "ServiceID", serviceID); err != nil {
		return nil, fmt.Errorf("set ServiceID: %w", err)
	}

	// Missing: applicable + not installed.
	missing, err := searchUpdates(searcher, "IsInstalled=0")
	if err != nil {
		return nil, err
	}
	// Offline cab scans return superseded updates as applicable. Flag (don't drop)
	// any whose replacement is also in this set, so the console can show the
	// superseded category while the actionable totals exclude them.
	superseded := markSuperseded(missing)
	var actionable int
	var actionableMB float64
	for _, u := range missing {
		if u.Superseded {
			continue
		}
		actionable++
		actionableMB += u.SizeMB
	}

	// Installed: the AUTHORITATIVE installed-updates view (WUA — what the system and
	// a UEM console show), NOT the coarse CBS-registry list. Catalog-scoped, so it
	// is the set relevant to patch compliance.
	installed, err := searchUpdates(searcher, "IsInstalled=1")
	if err != nil {
		slog.Warn("offline scan: installed-updates search failed (missing still reported)", "error", err.Error())
		installed = nil
	}

	slog.Info("offline scan complete",
		"applicable_missing", len(missing),
		"actionable_missing", actionable,
		"superseded", superseded,
		"installed", len(installed),
		"actionable_download_mb", fmt.Sprintf("%.1f", actionableMB))

	return &OfflineScanResult{Missing: missing, Installed: installed}, nil
}

// OfflineScanResult is the outcome of an offline cab scan: the not-installed
// applicable updates (missing; superseded ones tagged) and the installed updates
// (the authoritative WUA view — what the system/console shows).
type OfflineScanResult struct {
	Missing   []Update
	Installed []Update
}

// searchUpdates runs one WUA search against the already-scoped (offline cab)
// searcher and returns the updates found.
func searchUpdates(searcher *ole.IDispatch, criteria string) ([]Update, error) {
	slog.Info("offline scan: searching catalog", "criteria", criteria)
	stop := startHeartbeat("offline WUA search")
	resultRaw, err := oleutil.CallMethod(searcher, "Search", criteria)
	stop()
	if err != nil {
		if oleErr, ok := err.(*ole.OleError); ok {
			return nil, fmt.Errorf("offline search %q failed: code=0x%X, %s", criteria, oleErr.Code(), oleErr.Description())
		}
		return nil, fmt.Errorf("offline search %q failed: %w", criteria, err)
	}
	result := resultRaw.ToIDispatch()
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

	out := make([]Update, 0, count)
	for i := 0; i < count; i++ {
		itemRaw, err := oleutil.GetProperty(updates, "Item", i)
		if err != nil {
			slog.Warn("offline scan: could not read update item, skipping", "index", i, "of", count, "error", err.Error())
			continue
		}
		item := itemRaw.ToIDispatch()
		out = append(out, buildUpdate(item))
		item.Release()
	}
	return out, nil
}
