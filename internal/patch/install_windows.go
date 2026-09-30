//go:build windows

package patch

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// normalizeKB strips an optional, case-insensitive "KB" prefix (and surrounding
// whitespace) so callers may pass either "KB5126052" or "5126052". WUA stores
// KBArticleIDs as bare numbers, so matching is done against the normalized form.
func normalizeKB(kb string) string {
	kb = strings.TrimSpace(kb)
	if len(kb) >= 2 && strings.EqualFold(kb[:2], "kb") {
		kb = strings.TrimSpace(kb[2:])
	}
	return kb
}

type InstallResult struct {
	KB              string
	Success         bool
	RebootRequired  bool
	ErrorMessage    string
}

// InstallWindowsUpdate downloads and installs a single update identified
// by its KB number. Matches it against currently-missing updates (from a
// fresh search) rather than trusting a cached list — the update set can
// change between scan and install.
//
// DEV/TEST ONLY — this DOWNLOADS from Windows Update over the internet, which
// the no-internet rule forbids on production endpoints. Production remediation
// uses InstallLocalPatch (DISM on a server-delivered local file). Kept for
// local development/verification only.
func InstallWindowsUpdate(kb string) (*InstallResult, error) {
	kb = normalizeKB(kb)
	slog.Info("patch install starting", "kb", kb)
	cleanup, err := initCOM()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	unknown, err := oleutil.CreateObject("Microsoft.Update.Session")
	if err != nil {
		return nil, fmt.Errorf("failed to create Update.Session: %v", err)
	}
	defer unknown.Release()

	session, err := unknown.QueryInterface(ole.IID_IDispatch)
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

	slog.Info("install: searching current missing updates to locate KB", "kb", kb)
	stopSearch := startHeartbeat("WUA catalog search (install)")
	resultRaw, err := oleutil.CallMethod(searcher, "Search", "IsInstalled=0 and IsHidden=0")
	stopSearch()
	if err != nil {
		return nil, fmt.Errorf("search failed: %v", err)
	}
	result := resultRaw.ToIDispatch()
	defer result.Release()

	updatesRaw, err := oleutil.GetProperty(result, "Updates")
	if err != nil {
		return nil, err
	}
	updates := updatesRaw.ToIDispatch()
	defer updates.Release()

	countRaw, _ := oleutil.GetProperty(updates, "Count")
	count := int(countRaw.Val)

	// Find the update matching the requested KB.
	var target *ole.IDispatch
	for i := 0; i < count; i++ {
		itemRaw, err := oleutil.GetProperty(updates, "Item", i)
		if err != nil {
			slog.Warn("install: could not read update item, skipping", "index", i, "of", count, "error", err.Error())
			continue
		}
		item := itemRaw.ToIDispatch()

		if kbRaw, err := oleutil.GetProperty(item, "KBArticleIDs"); err == nil {
			kbColl := kbRaw.ToIDispatch()
			if kbCountRaw, err := oleutil.GetProperty(kbColl, "Count"); err == nil && int(kbCountRaw.Val) > 0 {
				if kbItem, err := oleutil.GetProperty(kbColl, "Item", 0); err == nil {
					if kbItem.ToString() == kb {
						target = item
						kbColl.Release()
						break
					}
				}
			}
			kbColl.Release()
		}
		item.Release()
	}

	if target == nil {
		slog.Warn("install: KB not found in current missing list (nothing to install)", "kb", kb, "missing_count", count)
		return &InstallResult{KB: kb, Success: false, ErrorMessage: "update not found in current missing list"}, nil
	}
	defer target.Release()
	slog.Info("install: KB matched in missing list", "kb", kb)

	// Accept EULA if required (common for driver/optional updates).
	oleutil.PutProperty(target, "AcceptEula", true)

	// Build a collection containing just this one update.
	collRaw, err := oleutil.CreateObject("Microsoft.Update.UpdateColl")
	if err != nil {
		return nil, fmt.Errorf("failed to create update collection: %v", err)
	}
	coll, err := collRaw.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return nil, err
	}
	defer coll.Release()
	oleutil.CallMethod(coll, "Add", target)

	// Download.
	downloaderRaw, err := oleutil.CallMethod(session, "CreateUpdateDownloader")
	if err != nil {
		return nil, fmt.Errorf("failed to create downloader: %v", err)
	}
	downloader := downloaderRaw.ToIDispatch()
	defer downloader.Release()
	oleutil.PutProperty(downloader, "Updates", coll)

	slog.Info("install: downloading update", "kb", kb)
	stopDownload := startHeartbeat("WUA download")
	_, downloadErr := oleutil.CallMethod(downloader, "Download")
	stopDownload()
	if downloadErr != nil {
		slog.Error("install: download failed", "kb", kb, "error", downloadErr.Error())
		return &InstallResult{KB: kb, Success: false, ErrorMessage: fmt.Sprintf("download failed: %v", downloadErr)}, nil
	}
	slog.Info("install: download complete", "kb", kb)

	// Install.
	installerRaw, err := oleutil.CallMethod(session, "CreateUpdateInstaller")
	if err != nil {
		return nil, fmt.Errorf("failed to create installer: %v", err)
	}
	installer := installerRaw.ToIDispatch()
	defer installer.Release()
	oleutil.PutProperty(installer, "Updates", coll)

	slog.Info("install: installing update", "kb", kb)
	stopInstall := startHeartbeat("WUA install")
	installResultRaw, installErr := oleutil.CallMethod(installer, "Install")
	stopInstall()
	if installErr != nil {
		slog.Error("install: install failed", "kb", kb, "error", installErr.Error())
		return &InstallResult{KB: kb, Success: false, ErrorMessage: fmt.Sprintf("install failed: %v", installErr)}, nil
	}
	installResult := installResultRaw.ToIDispatch()
	defer installResult.Release()

	resultCodeRaw, _ := oleutil.GetProperty(installResult, "ResultCode")

	// ResultCode: 2 = succeeded, 3 = succeeded with errors, others = failed
	resultCode := int(resultCodeRaw.Val)
	success := resultCode == 2 || resultCode == 3

	// RebootRequired can come back as a non-bool/empty VARIANT; treat anything
	// that isn't an explicit true as false rather than panicking on assertion.
	rebootRequired := false
	if rebootRaw, err := oleutil.GetProperty(installResult, "RebootRequired"); err == nil {
		if b, ok := rebootRaw.Value().(bool); ok {
			rebootRequired = b
		}
	}

	slog.Info("patch install finished",
		"kb", kb,
		"success", success,
		"reboot_required", rebootRequired,
		"wua_result_code", resultCode)

	return &InstallResult{
		KB:             kb,
		Success:        success,
		RebootRequired: rebootRequired,
		ErrorMessage:   fmt.Sprintf("WUA ResultCode=%d", resultCode),
	}, nil
}