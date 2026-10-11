//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/buffer"
	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	"github.com/aegisoneuem/aegis_one_agent/internal/patch"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

// runPatchDetection launches the two detection layers; both only ever write to
// the buffer (the sender goroutine drains it), never the network.
//   - Layer 1 (frequent): lightweight installed-KB/build report, sent on change.
//   - Layer 4 (weekly): the heavier offline cab reconciliation scan (needs a cab).
func runPatchDetection(cfg config.Config, buf buffer.Buffer, baseline *baselineStore, rebootStore *rebootPendingStore, scanTrigger <-chan struct{}, drainSignal chan<- struct{}) {
	go runDeviceStateReport(cfg, buf, baseline, rebootStore, drainSignal)
	if cfg.WsusScanCab == "" {
		slog.Info("patch compliance scan disabled (no AEGIS_WSUS_SCAN_CAB configured)")
		return
	}
	go runReconciliationScan(cfg, buf, scanTrigger, drainSignal)
}

// runDeviceStateReport is Layer 1: read installed KBs + build locally (registry
// only, milliseconds, no COM/WUA, no network) and buffer a lean report — but only
// when it CHANGED since the last send (the installed set rarely changes, so
// sending every cycle would be wasteful at fleet scale). The agent reports
// installed facts only; the server computes "missing".
func runDeviceStateReport(cfg config.Config, buf buffer.Buffer, baseline *baselineStore, rebootStore *rebootPendingStore, drainSignal chan<- struct{}) {
	var lastFingerprint string
	var lastSelfDiffKey string
	report := func() {
		state, err := patch.CollectLocalState()
		if err != nil {
			// Fail loud: unknown local state is not "nothing installed".
			slog.Error("FAST-LANE patch-state: could not read local state", "error", err.Error())
			return
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

		rep := &pb.DeviceStateReport{
			DeviceId:             cfg.DeviceID,
			ProductName:          state.ProductName,
			DisplayVersion:       state.DisplayVersion,
			Build:                state.FullBuild,
			Architecture:         state.Architecture,
			CollectedAt:          time.Now().Unix(),
			InstalledKbs:         state.InstalledKBs,
			RebootPending:        rebootPending,
			RebootPendingKbs:     pendingKBs,
			RebootPendingSources: rebootSources,
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
		slog.Info("FAST-LANE patch-state: CHANGED -> sending", "build", rep.Build, "installed_kbs", len(rep.InstalledKbs), "kbs", rep.InstalledKbs, "reboot_pending", rep.RebootPending)
		signalDrain(drainSignal)
	}

	safeRun("layer1-device-state", report) // once at startup, then on the interval
	ticker := time.NewTicker(cfg.DeviceStateInterval)
	defer ticker.Stop()
	for range ticker.C {
		safeRun("layer1-device-state", report)
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
// detection (build + architecture + the installed-KB set, which is already sorted).
func deviceStateFingerprint(rep *pb.DeviceStateReport) string {
	h := sha256.New()
	h.Write([]byte(rep.Build))
	h.Write([]byte{'|'})
	h.Write([]byte(rep.Architecture))
	h.Write([]byte{'|'})
	for _, kb := range rep.InstalledKbs {
		h.Write([]byte(kb))
		h.Write([]byte{','})
	}
	// Include pending-reboot state so a newly-pending (or post-reboot cleared)
	// device is reported promptly, not suppressed as "unchanged".
	h.Write([]byte{'|'})
	if rep.RebootPending {
		h.Write([]byte("reboot-pending"))
	}
	for _, kb := range rep.RebootPendingKbs {
		h.Write([]byte(kb))
		h.Write([]byte{','})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// runReconciliationScan is Layer 4: the occasional (weekly) heavier offline cab
// scan that reports MISSING updates directly, as a cross-check against the
// server's Layer 1 diff. Uses only the local cab on disk — never the internet.
func runReconciliationScan(cfg config.Config, buf buffer.Buffer, scanTrigger <-chan struct{}, drainSignal chan<- struct{}) {
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
		actionID := fmt.Sprintf("scan-%d", time.Now().Unix())
		started := time.Now().Unix()
		slog.Info("FULL-SCAN (cab): starting offline cab scan", "cab", cfg.WsusScanCab)
		updates, err := patch.ScanOffline(cfg.WsusScanCab)
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
		result := buildScanResult(cfg, updates)
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
			"missing", len(result.Missing), "actionable", actionable, "superseded", superseded, "kbs", missingKBs)
		bufferActionStatus(&pb.ActionStatus{
			ActionId: actionID, ActionType: actionScan, DeviceId: cfg.DeviceID,
			Phase:  pb.ActionPhase_ACTION_PHASE_SUCCESS,
			Reason: fmt.Sprintf("%d applicable (%d missing, %d superseded)", len(result.Missing), actionable, superseded),
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

func buildScanResult(cfg config.Config, updates []patch.Update) *pb.PatchScanResult {
	state, err := patch.CollectLocalState()
	if err != nil {
		slog.Warn("reconciliation (Layer 4): could not read local state for report", "error", err.Error())
		state = &patch.LocalState{}
	}

	missing := make([]*pb.MissingUpdate, 0, len(updates))
	for _, u := range updates {
		missing = append(missing, &pb.MissingUpdate{
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

	return &pb.PatchScanResult{
		DeviceId:       cfg.DeviceID,
		ProductName:    state.ProductName,
		DisplayVersion: state.DisplayVersion,
		Build:          state.FullBuild,
		Architecture:   state.Architecture,
		CollectedAt:    time.Now().Unix(),
		Source:         "offline-cab",
		Missing:        missing,
	}
}
