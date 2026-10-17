//go:build windows

package patch

import (
	"fmt"
	"log/slog"
	"time"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// UninstallKB removes an installed update by KB using the Windows Update Agent
// (WUA) COM API. NOTE: it is no longer in the default uninstall chain — runUninstall
// uses DISM (registry-resolved package) then wusa, matching BigFix, because the WUA
// COM Uninstall call is synchronous and can BLOCK for 30+ minutes (or hang) on a
// cumulative update. UninstallKB is retained as a tested, manual/future fallback.
//
// It is deliberately NOT wusa.exe for its own mechanism. wusa's silent uninstall
// (`/uninstall /kb:N /quiet`) returns ERROR_INVALID_PARAMETER (0x57) when run from
// the agent's non-interactive LocalSystem service (session 0), even though the
// exact same command works in an interactive admin session. The WUA API works in
// session 0, needs NO internet (Online=false → it searches only the local
// installed-updates store), honors IsUninstallable, and uses no PowerShell.
//
// The timeout is advisory: the WUA Uninstall call is a synchronous in-process COM
// call that cannot be force-cancelled (a heartbeat logs progress meanwhile). The
// caller should confirm the KB is installed first (idempotency).
func UninstallKB(kb string, timeout time.Duration) *DeployResult {
	kb = normalizeKB(kb) // strip optional "KB" prefix -> digits
	if kb == "" {
		return &DeployResult{Reason: "no KB specified for uninstall"}
	}
	slog.Info("deploy: uninstalling update via WUA (offline, service-context safe)", "kb", "KB"+kb)

	cleanup, err := initCOM()
	if err != nil {
		return &DeployResult{Reason: "COM init failed: " + err.Error()}
	}
	defer cleanup()

	sessRaw, err := oleutil.CreateObject("Microsoft.Update.Session")
	if err != nil {
		return &DeployResult{Reason: "create Update.Session: " + err.Error()}
	}
	defer sessRaw.Release()
	session, err := sessRaw.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return &DeployResult{Reason: err.Error()}
	}
	defer session.Release()

	searcherRaw, err := oleutil.CallMethod(session, "CreateUpdateSearcher")
	if err != nil {
		return &DeployResult{Reason: "CreateUpdateSearcher: " + err.Error()}
	}
	searcher := searcherRaw.ToIDispatch()
	defer searcher.Release()
	// Offline: search ONLY the local installed-updates store — never the internet
	// (the agent must never reach Windows Update directly).
	if _, err := oleutil.PutProperty(searcher, "Online", false); err != nil {
		return &DeployResult{Reason: "set searcher Online=false: " + err.Error()}
	}

	match, reason := findInstalledUpdateByKB(searcher, kb)
	if match == nil {
		// Not among installed updates → nothing to uninstall (idempotent no-op), not a
		// failure. The caller maps this to NOT_APPLICABLE.
		return &DeployResult{NotApplicable: true, Reason: reason}
	}
	defer match.Release()

	// NOTE: IsUninstallable is WUA METADATA and is NOT reliable — it reports false
	// for updates that Control Panel (which shells out to wusa / CBS) can in fact
	// remove (observed on KB5122882). So we do NOT refuse on it; we attempt the
	// uninstall and report the REAL result from the operation. We only log the hint.
	if !boolProp(match, "IsUninstallable") {
		slog.Info("deploy: WUA marks IsUninstallable=false, but that flag is unreliable — attempting the uninstall anyway", "kb", "KB"+kb)
	}

	// Build a one-item collection and uninstall it via IUpdateInstaller.
	collRaw, err := oleutil.CreateObject("Microsoft.Update.UpdateColl")
	if err != nil {
		return &DeployResult{Reason: "create UpdateColl: " + err.Error()}
	}
	defer collRaw.Release()
	coll, err := collRaw.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return &DeployResult{Reason: err.Error()}
	}
	defer coll.Release()
	if _, err := oleutil.CallMethod(coll, "Add", match); err != nil {
		return &DeployResult{Reason: "add update to collection: " + err.Error()}
	}

	installerRaw, err := oleutil.CallMethod(session, "CreateUpdateInstaller")
	if err != nil {
		return &DeployResult{Reason: "CreateUpdateInstaller: " + err.Error()}
	}
	installer := installerRaw.ToIDispatch()
	defer installer.Release()
	if _, err := oleutil.PutProperty(installer, "Updates", coll); err != nil {
		return &DeployResult{Reason: "set installer.Updates: " + err.Error()}
	}

	slog.Info("deploy: WUA uninstall starting", "kb", "KB"+kb)
	stop := startHeartbeat("WUA uninstall")
	resRaw, err := oleutil.CallMethod(installer, "Uninstall")
	stop()
	if err != nil {
		if oleErr, ok := err.(*ole.OleError); ok {
			return &DeployResult{Reason: fmt.Sprintf("WUA uninstall call failed: code=0x%X, %s", oleErr.Code(), oleErr.Description())}
		}
		return &DeployResult{Reason: "WUA uninstall call failed: " + err.Error()}
	}
	res := resRaw.ToIDispatch()
	defer res.Release()

	// IUninstallationResult: the top-level ResultCode/HResult are an AGGREGATE
	// (0x80240022 WU_E_ALL_UPDATES_FAILED just means "all updates in the batch
	// failed"). The actionable per-update code is in GetUpdateResult(0).HResult —
	// read it so the failure reason is specific, not the generic aggregate.
	resultCode := intProp(res, "ResultCode")
	hresult := intProp(res, "HResult")
	reboot := boolProp(res, "RebootRequired")
	if perRaw, err := oleutil.CallMethod(res, "GetUpdateResult", 0); err == nil {
		per := perRaw.ToIDispatch()
		if h := intProp(per, "HResult"); h != 0 {
			hresult = h // the specific per-update code is more useful than the aggregate
		}
		per.Release()
	}

	out := &DeployResult{ExitCode: hresult, RebootRequired: reboot}
	switch resultCode {
	case 2, 3: // orcSucceeded / orcSucceededWithErrors
		out.Success = true
		slog.Info("deploy: uninstall succeeded (WUA)", "kb", "KB"+kb, "result_code", resultCode, "reboot_required", reboot)
	default:
		out.Reason = uninstallFailureReason(hresult)
		slog.Error("deploy: uninstall FAILED (WUA)", "kb", "KB"+kb,
			"result_code", resultCode, "hresult", fmt.Sprintf("0x%X", uint32(hresult)), "reason", out.Reason)
	}
	return out
}

// findInstalledUpdateByKB searches the local installed-updates store for an update
// whose KB matches, and returns the matching update's IDispatch (caller releases)
// or nil + a human reason. Online=false must already be set on the searcher.
func findInstalledUpdateByKB(searcher *ole.IDispatch, kb string) (*ole.IDispatch, string) {
	slog.Info("deploy: searching installed updates (WUA, offline)", "match_kb", "KB"+kb)
	stop := startHeartbeat("WUA installed-updates search")
	resultRaw, err := oleutil.CallMethod(searcher, "Search", "IsInstalled=1")
	stop()
	if err != nil {
		if oleErr, ok := err.(*ole.OleError); ok {
			return nil, fmt.Sprintf("WUA installed-updates search failed: code=0x%X, %s", oleErr.Code(), oleErr.Description())
		}
		return nil, "WUA installed-updates search failed: " + err.Error()
	}
	result := resultRaw.ToIDispatch()
	defer result.Release()

	updatesRaw, err := oleutil.GetProperty(result, "Updates")
	if err != nil {
		return nil, "read Updates: " + err.Error()
	}
	updates := updatesRaw.ToIDispatch()
	defer updates.Release()

	countRaw, err := oleutil.GetProperty(updates, "Count")
	if err != nil {
		return nil, "read Updates.Count: " + err.Error()
	}
	count := int(countRaw.Val)

	for i := 0; i < count; i++ {
		itemRaw, err := oleutil.GetProperty(updates, "Item", i)
		if err != nil {
			continue
		}
		item := itemRaw.ToIDispatch()
		for _, k := range stringCollection(item, "KBArticleIDs") {
			if normalizeKB(k) == kb {
				return item, "" // matched — caller releases this IDispatch
			}
		}
		item.Release()
	}
	return nil, fmt.Sprintf("KB%s not found among installed updates (nothing to uninstall, or Windows Update does not track it)", kb)
}

// uninstallFailureReason turns a WUA uninstall HResult into a concise, human
// cause. The common case on a freshly-deployed OS is that the update was baked
// into the image (or is the latest cumulative), so Windows has no removal data
// for it — reported honestly rather than as a bare hex code.
func uninstallFailureReason(hresult int) string {
	h := uint32(hresult)
	switch h {
	case 0x80240022: // WU_E_ALL_UPDATES_FAILED
		return "Windows Update could not uninstall this update (0x80240022) — commonly a cumulative/baked-in update WUA cannot remove. If removal is required, try Control Panel → Installed Updates (manual) which uses a different (wusa/CBS) path."
	case 0x80070002: // ERROR_FILE_NOT_FOUND
		return "Windows could not uninstall this update (0x80070002): removal source files are not present (the update has no uninstall data on this system)."
	case 0x8024200B: // WU_E_UH_INSTALLERFAILURE
		return "Windows update handler failed to uninstall this update (0x8024200B)."
	case 0x80070520, 0x80070522:
		return fmt.Sprintf("Windows denied the uninstall (0x%X) — insufficient privilege or policy restriction.", h)
	default:
		return fmt.Sprintf("Windows could not uninstall this update (0x%X).", h)
	}
}

// boolProp reads a VARIANT_BOOL property, false on any error.
func boolProp(disp *ole.IDispatch, name string) bool {
	if v, err := oleutil.GetProperty(disp, name); err == nil {
		return v.Val != 0
	}
	return false
}

// intProp reads an integer property, 0 on any error.
func intProp(disp *ole.IDispatch, name string) int {
	if v, err := oleutil.GetProperty(disp, name); err == nil {
		return int(v.Val)
	}
	return 0
}
