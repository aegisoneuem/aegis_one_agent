//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/buffer"
	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	"github.com/aegisoneuem/aegis_one_agent/internal/patch"
	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

// runPatchDetection launches patch detection; both layers only ever write to
// the buffer (the sender goroutine drains it), never the network.
//
//   - Layer 1 (PRIMARY): lightweight build + installed-KB report, sent on
//     change. The server diffs this against its server-side catalog to compute
//     "missing" — the endpoint never needs a cab. This is the scalable path
//     (see docs/server-spec-patch-detection.md).
//   - Layer 4 (OPTIONAL reconciliation): the heavier offline cab scan. DEMOTED
//     — it needs a local wsusscn2.cab and is OFF by default. Enable it only for
//     air-gapped sites or on-demand deep reconciliation by setting
//     AEGIS_WSUS_SCAN_CAB. Do NOT distribute the cab to the fleet for routine
//     detection; that is exactly the scaling problem Layer 1 removes.
func runPatchDetection(cfg config.Config, buf buffer.Buffer, baseline *baselineStore, rebootStore *rebootPendingStore, installedSet *installedStore, scanTrigger <-chan struct{}, deviceStateTrigger <-chan struct{}, drainSignal chan<- struct{}) {
	go runDeviceStateReport(cfg, buf, baseline, rebootStore, installedSet, deviceStateTrigger, drainSignal)
	if cfg.WsusScanCab == "" {
		// Expected default in the server-side-diff model — NOT a misconfiguration.
		slog.Info("patch detection: Layer 1 (server-side-diff) active; Layer 4 offline cab scan OFF by default (set AEGIS_WSUS_SCAN_CAB only for air-gapped/on-demand reconciliation)")
		return
	}
	slog.Info("patch detection: Layer 4 offline cab reconciliation ENABLED (optional path)", "cab", cfg.WsusScanCab)
	go runReconciliationScan(cfg, buf, installedSet, scanTrigger, drainSignal)
}

// runDeviceStateReport is Layer 1: read installed KBs + build locally (registry
// only, milliseconds, no COM/WUA, no network) and buffer a lean report — but only
// when it CHANGED since the last send (the installed set rarely changes, so
// sending every cycle would be wasteful at fleet scale). The agent reports
// installed facts only; the server computes "missing".
func runDeviceStateReport(cfg config.Config, buf buffer.Buffer, baseline *baselineStore, rebootStore *rebootPendingStore, installedSet *installedStore, deviceStateTrigger <-chan struct{}, drainSignal chan<- struct{}) {
	var lastFingerprint string
	var lastSelfDiffKey string
	// Full installed-KB set (WUA ∪ QFE ∪ CBS) is heavier than the cheap CBS
	// build read, so it is refreshed on a TTL (or forced on-demand / after a
	// servicing change), cached, and reused between refreshes. installedSet
	// also persists it for the install worker's "already installed?" check.
	var cachedInstalledKBs []string
	var lastInstalledRefresh time.Time
	const installedRefreshTTL = 1 * time.Hour
	report := func(forceInstalledRefresh bool) {
		state, err := patch.CollectLocalState()
		if err != nil {
			// Fail loud: unknown local state is not "nothing installed".
			slog.Error("FAST-LANE patch-state: could not read local state", "error", err.Error())
			return
		}

		// Refresh the COMPLETE installed-KB set when forced, uninitialized, or
		// the TTL elapsed. This is the appwiz.cpl-parity set (CBS alone misses
		// cumulative updates). A failure falls back to the cheap CBS set so the
		// report still goes out — never block on it.
		if forceInstalledRefresh || cachedInstalledKBs == nil || time.Since(lastInstalledRefresh) > installedRefreshTTL {
			if kbs, ierr := patch.CollectInstalledKBs(); ierr != nil {
				slog.Warn("FAST-LANE patch-state: full installed-KB collection failed; using CBS fallback", "error", ierr.Error())
				if cachedInstalledKBs == nil {
					cachedInstalledKBs = state.InstalledKBs
				}
			} else {
				cachedInstalledKBs = kbs
				lastInstalledRefresh = time.Now()
				installedSet.replace(kbs) // persist for the install worker's pre-check
			}
		}

		// Best-effort offline self-diff against the server's cached baseline —
		// logged only when the likely-missing set CHANGES (avoids per-cycle noise).
		likely := offlineSelfDiff(baseline, state)
		if key := strings.Join(likely, ","); key != lastSelfDiffKey {
			if len(likely) > 0 {
				slog.Warn("offline self-diff: likely missing update(s) vs cached baseline (best-effort; server confirms on reconnect)",
					"count", len(likely), "kbs", likely)
			} else {
				slog.Info("offline self-diff: device now appears current vs cached baseline")
			}
			lastSelfDiffKey = key
		}
		// Device-level pending-reboot (cheap registry read). If nothing is pending,
		// the reboot has happened → clear the tracked KBs (they are now effective).
		rebootPending, rebootSources := patch.RebootPending()
		var pendingKBs []string
		if rebootPending {
			pendingKBs = rebootStore.list()
		} else {
			rebootStore.clear()
		}

		// Fast lane carries only cheap, authoritative signals: the OS build (the
		// authoritative cumulative level) + pending-reboot. The installed-updates
		// LIST is NOT sent here (the coarse CBS list was misleading) — the
		// authoritative installed set comes from the full scan (PatchScanResult).
		// Enriched installed-apps inventory for the server's third-party patch
		// catalog. The agent reports facts only; catalog policy (which apps the
		// server tracks, which are outdated, what the latest version is) is
		// entirely server-team territory. Enumeration is best-effort — a
		// registry read failure here NEVER blocks the fast-lane KB report.
		var installedApps []*pb.InstalledApp
		if apps, aerr := softdist.EnumerateInstalledApps(); aerr != nil {
			slog.Warn("FAST-LANE patch-state: could not enumerate installed apps for inventory", "error", aerr.Error())
		} else {
			installedApps = make([]*pb.InstalledApp, 0, len(apps))
			for _, a := range apps {
				installedApps = append(installedApps, &pb.InstalledApp{
					DisplayName:     a.DisplayName,
					DisplayVersion:  a.DisplayVersion,
					Publisher:       a.Publisher,
					InstallDate:     a.InstallDate,
					UninstallKey:    a.Key,
					Hive:            a.RegistryView,
					EstimatedSizeKb: a.EstimatedSizeKB,
					UninstallString: a.UninstallString,
				})
			}
		}
		rep := &pb.DeviceStateReport{
			DeviceId:             cfg.DeviceID,
			ProductName:          state.ProductName,
			DisplayVersion:       state.DisplayVersion,
			Build:                state.FullBuild,
			Architecture:         state.Architecture,
			CollectedAt:          time.Now().Unix(),
			RebootPending:        rebootPending,
			RebootPendingKbs:     pendingKBs,
			RebootPendingSources: rebootSources,
			InstalledApps:        installedApps,
			// Complete installed-KB set (WUA ∪ QFE ∪ CBS, no cab) — the diff
			// input the SERVER needs to compute "missing" without shipping a cab
			// to the endpoint. appwiz.cpl parity (CBS alone missed cumulative
			// updates). See docs/server-spec-patch-detection.md.
			InstalledKbs: cachedInstalledKBs,
		}
		fp := deviceStateFingerprint(rep)
		if fp == lastFingerprint {
			slog.Debug("FAST-LANE patch-state: no change this cycle (skip send)", "build", rep.Build, "interval", cfg.DeviceStateInterval.String())
			return
		}
		payload, err := proto.Marshal(rep)
		if err != nil {
			slog.Error("FAST-LANE patch-state: marshal failed", "error", err.Error())
			return
		}
		if err := buf.Enqueue(kindDeviceState, payload); err != nil {
			slog.Error("FAST-LANE patch-state: could not buffer report", "error", err.Error())
			return
		}
		lastFingerprint = fp
		slog.Info("FAST-LANE patch-state: CHANGED -> sending", "build", rep.Build, "reboot_pending", rep.RebootPending, "installed_apps", len(rep.InstalledApps), "installed_kbs", len(rep.InstalledKbs))
		signalDrain(drainSignal)
	}

	// Startup: force a full installed-KB refresh to populate the set.
	safeRun("layer1-device-state", func() { report(true) })
	ticker := time.NewTicker(cfg.DeviceStateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			// Periodic tick: TTL governs whether the heavy set is re-collected.
			safeRun("layer1-device-state", func() { report(false) })
		case <-deviceStateTrigger:
			// On-demand refresh (signed report_device_state command, OR a CBS
			// servicing change = install/uninstall). Reset the fingerprint AND
			// force a full installed-KB recollection so a just-installed/removed
			// patch is reflected immediately and the server re-diffs.
			slog.Info("FAST-LANE patch-state: on-demand/servicing refresh — resetting fingerprint + recollecting installed KBs")
			lastFingerprint = ""
			safeRun("layer1-device-state", func() { report(true) })
		}
	}
}

// offlineSelfDiff compares installed KBs against the server's cached baseline and
// returns the "likely missing" KBs — a best-effort hint (most useful while
// offline); the authoritative diff is server-side on reconnect. Returns nil when
// no baseline is cached, it is for a different build, or nothing is missing. The
// caller logs (deduped) so this stays side-effect free.
func offlineSelfDiff(baseline *baselineStore, state *patch.LocalState) []string {
	bl, err := baseline.load()
	if err != nil {
		slog.Debug("offline self-diff: could not read cached baseline", "error", err.Error())
		return nil
	}
	if bl == nil {
		return nil // no baseline cached yet
	}
	// Trim defensively — don't trust the server/env to send a perfectly
	// formatted build string (stray whitespace must not break the match).
	if strings.TrimSpace(bl.Build) == "" || strings.TrimSpace(bl.Build) != strings.TrimSpace(state.FullBuild) {
		slog.Debug("offline self-diff: baseline is for a different build, skipping",
			"baseline_build", bl.Build, "device_build", state.FullBuild)
		return nil
	}
	installed := make(map[string]bool, len(state.InstalledKBs))
	for _, kb := range state.InstalledKBs {
		installed[normalizeKB(kb)] = true
	}
	var likelyMissing []string
	for _, kb := range bl.LatestKBs {
		if !installed[normalizeKB(kb)] {
			likelyMissing = append(likelyMissing, kb)
		}
	}
	return likelyMissing
}

// deviceStateFingerprint is a stable hash of the fields that matter for change
// detection (build + architecture + reboot state + the installed-apps inventory).
// A newly installed/uninstalled/upgraded app triggers a resend so the server
// catalog stays in sync without waiting for the weekly reconciliation scan.
func deviceStateFingerprint(rep *pb.DeviceStateReport) string {
	h := sha256.New()
	h.Write([]byte(rep.Build))
	h.Write([]byte{'|'})
	h.Write([]byte(rep.Architecture))
	// Pending-reboot state so a newly-pending (or post-reboot cleared) device is
	// reported promptly, not suppressed as "unchanged".
	h.Write([]byte{'|'})
	if rep.RebootPending {
		h.Write([]byte("reboot-pending"))
	}
	for _, kb := range rep.RebootPendingKbs {
		h.Write([]byte(kb))
		h.Write([]byte{','})
	}
	// Installed-KB set drives change detection in the server-side-diff model:
	// an install/uninstall changes this set, which MUST trigger a prompt Layer 1
	// resend so the server re-diffs against its catalog. CollectLocalState
	// returns the set already sorted + deduped, so the hash is stable.
	h.Write([]byte{'|'})
	for _, kb := range rep.InstalledKbs {
		h.Write([]byte(kb))
		h.Write([]byte{','})
	}
	// Installed-apps inventory: hash only (hive, uninstall_key, display_name,
	// display_version). Install_date and estimated_size churn too easily to
	// drive change detection (install_date can refresh on repair, size can
	// drift on cache updates). Sort by (hive, key) so enumeration order never
	// causes a false fingerprint mismatch.
	apps := make([]*pb.InstalledApp, len(rep.InstalledApps))
	copy(apps, rep.InstalledApps)
	sort.Slice(apps, func(i, j int) bool {
		if apps[i].Hive != apps[j].Hive {
			return apps[i].Hive < apps[j].Hive
		}
		return apps[i].UninstallKey < apps[j].UninstallKey
	})
	h.Write([]byte{'|'})
	for _, a := range apps {
		h.Write([]byte(a.Hive))
		h.Write([]byte{0x1F}) // unit separator
		h.Write([]byte(a.UninstallKey))
		h.Write([]byte{0x1F})
		h.Write([]byte(a.DisplayName))
		h.Write([]byte{0x1F})
		h.Write([]byte(a.DisplayVersion))
		h.Write([]byte{0x1E}) // record separator
	}
	return hex.EncodeToString(h.Sum(nil))
}

// runReconciliationScan is Layer 4: the occasional (weekly) heavier offline cab
// scan that reports MISSING updates directly, as a cross-check against the
// server's Layer 1 diff. Uses only the local cab on disk — never the internet.
func runReconciliationScan(cfg config.Config, buf buffer.Buffer, installedSet *installedStore, scanTrigger <-chan struct{}, drainSignal chan<- struct{}) {
	// bufferActionStatus queues a per-scan ActionStatus so the console can show the
	// compliance-scan run in its unified per-action view (not just the result data).
	bufferActionStatus := func(st *pb.ActionStatus) {
		payload, err := proto.Marshal(st)
		if err != nil {
			slog.Warn("FULL-SCAN (cab): could not marshal action status", "error", err.Error())
			return
		}
		if err := buf.Enqueue(kindActionStatus, payload); err != nil {
			slog.Warn("FULL-SCAN (cab): could not buffer action status", "error", err.Error())
			return
		}
		signalDrain(drainSignal)
	}

	scan := func() {
		// Never run the heavy scan while the agent is installing/uninstalling — they
		// contend on WU/TrustedInstaller (slow install + half-applied scan data). If a
		// servicing op is in progress, defer: the CBS watch re-triggers a scan once it
		// settles, so the scan is not lost.
		if servicingBusy.Load() {
			slog.Info("FULL-SCAN (cab): deferred — an install/uninstall is in progress; will re-scan after it settles")
			return
		}
		if !servicingGate.TryLock() {
			slog.Info("FULL-SCAN (cab): deferred — a servicing op just started; will re-scan after it settles")
			return
		}
		defer servicingGate.Unlock()

		actionID := fmt.Sprintf("scan-%d", time.Now().Unix())
		started := time.Now().Unix()
		slog.Info("FULL-SCAN (cab): starting offline cab scan", "cab", cfg.WsusScanCab)
		scan, err := patch.ScanOffline(cfg.WsusScanCab)
		if err != nil {
			// Fail loud: an errored scan is UNKNOWN, never silently "compliant".
			slog.Error("FULL-SCAN (cab): scan failed (patch status UNKNOWN, not compliant)", "error", err.Error())
			bufferActionStatus(&pb.ActionStatus{
				ActionId: actionID, ActionType: actionScan, DeviceId: cfg.DeviceID,
				Phase: pb.ActionPhase_ACTION_PHASE_FAILED, Reason: "scan failed: " + err.Error(),
				StartedAt: started, FinishedAt: time.Now().Unix(),
			})
			return
		}
		result := buildScanResult(cfg, scan)
		payload, err := proto.Marshal(result)
		if err != nil {
			slog.Error("FULL-SCAN (cab): marshal failed", "error", err.Error())
			return
		}
		if err := buf.Enqueue(kindPatchScanResult, payload); err != nil {
			slog.Error("FULL-SCAN (cab): could not buffer result", "error", err.Error())
			return
		}
		// Count actionable vs superseded, and collect KB numbers, for the per-action
		// summary + a visible client-log list of what the scan found.
		var actionable, superseded int
		missingKBs := make([]string, 0, len(result.Missing))
		for _, m := range result.Missing {
			tag := "KB" + strings.Join(m.KbArticleIds, "/")
			if m.Superseded {
				superseded++
				tag += "(superseded)"
			} else {
				actionable++
			}
			missingKBs = append(missingKBs, tag)
		}
		slog.Info("FULL-SCAN (cab): result buffered for sending",
			"missing", actionable, "superseded", superseded, "installed", len(result.Installed), "missing_kbs", missingKBs)

		// Persist the authoritative installed-KB set so the install worker can RELIABLY
		// skip an already-applied patch (the cheap CBS check misses some updates).
		installedKBs := make([]string, 0, len(result.Installed))
		for _, u := range result.Installed {
			installedKBs = append(installedKBs, u.KbArticleIds...)
		}
		installedSet.replace(installedKBs)
		bufferActionStatus(&pb.ActionStatus{
			ActionId: actionID, ActionType: actionScan, DeviceId: cfg.DeviceID,
			Phase:  pb.ActionPhase_ACTION_PHASE_SUCCESS,
			Reason: fmt.Sprintf("%d missing, %d superseded, %d installed", actionable, superseded, len(result.Installed)),
			StartedAt: started, FinishedAt: time.Now().Unix(),
		})
		signalDrain(drainSignal)
	}

	slog.Info("FULL-SCAN (cab): running at startup (heavy; then only on weekly interval / new catalog / on-demand)")
	safeRun("patch-compliance-scan", scan) // once at startup, then on the interval OR a trigger
	ticker := time.NewTicker(cfg.ReconScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			slog.Info("FULL-SCAN (cab): running on the periodic interval", "interval", cfg.ReconScanInterval.String())
			safeRun("patch-compliance-scan", scan)
		case <-scanTrigger:
			// A new catalog (cab) landed (or an on-demand scan was requested) — run
			// the heavy scan now instead of waiting for the weekly tick. Install/
			// uninstall WITHOUT a cab change is handled cheaply by the fast lane.
			slog.Info("FULL-SCAN (cab): triggered (new catalog / on-demand)")
			safeRun("patch-compliance-scan", scan)
		}
	}
}

func buildScanResult(cfg config.Config, scan *patch.OfflineScanResult) *pb.PatchScanResult {
	state, err := patch.CollectLocalState()
	if err != nil {
		slog.Warn("FULL-SCAN (cab): could not read local state for report", "error", err.Error())
		state = &patch.LocalState{}
	}
	return &pb.PatchScanResult{
		DeviceId:       cfg.DeviceID,
		ProductName:    state.ProductName,
		DisplayVersion: state.DisplayVersion,
		Build:          state.FullBuild,
		Architecture:   state.Architecture,
		CollectedAt:    time.Now().Unix(),
		Source:         "offline-cab",
		Missing:        toMissingUpdates(scan.Missing),
		Installed:      toMissingUpdates(scan.Installed),
	}
}

// toMissingUpdates maps the rich internal Update metadata onto the wire type
// (used for both the missing and the installed lists).
func toMissingUpdates(updates []patch.Update) []*pb.MissingUpdate {
	out := make([]*pb.MissingUpdate, 0, len(updates))
	for _, u := range updates {
		out = append(out, &pb.MissingUpdate{
			UpdateId:            u.UpdateID,
			KbArticleIds:        u.KBArticleIDs,
			Title:               u.Title,
			Classification:      u.Classification,
			Products:            u.Products,
			Type:                u.Type,
			MsrcSeverity:        u.MsrcSeverity,
			CveIds:              u.CveIDs,
			SizeMb:              u.SizeMB,
			RebootRequired:      u.RebootRequired,
			Superseded:          u.Superseded,
			SupersededUpdateIds: u.SupersededUpdateIDs,
			SecurityBulletinIds: u.SecurityBulletinIDs,
			RevisionNumber:      int32(u.RevisionNumber),
			SupportUrl:          u.SupportURL,
			Description:         u.Description,
			MinSizeMb:           u.MinSizeMB,
		})
	}
	return out
}
