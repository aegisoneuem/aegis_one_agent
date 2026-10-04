//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
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
func runPatchDetection(cfg config.Config, buf buffer.Buffer, baseline *baselineStore) {
	go runDeviceStateReport(cfg, buf, baseline)
	if cfg.WsusScanCab == "" {
		slog.Info("reconciliation scan (Layer 4) disabled (no AEGIS_WSUS_SCAN_CAB configured)")
		return
	}
	go runReconciliationScan(cfg, buf)
}

// runDeviceStateReport is Layer 1: read installed KBs + build locally (registry
// only, milliseconds, no COM/WUA, no network) and buffer a lean report — but only
// when it CHANGED since the last send (the installed set rarely changes, so
// sending every cycle would be wasteful at fleet scale). The agent reports
// installed facts only; the server computes "missing".
func runDeviceStateReport(cfg config.Config, buf buffer.Buffer, baseline *baselineStore) {
	var lastFingerprint string
	var lastSelfDiffKey string
	report := func() {
		state, err := patch.CollectLocalState()
		if err != nil {
			// Fail loud: unknown local state is not "nothing installed".
			slog.Error("device-state (Layer 1): could not read local state", "error", err.Error())
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
		rep := &pb.DeviceStateReport{
			DeviceId:       cfg.DeviceID,
			ProductName:    state.ProductName,
			DisplayVersion: state.DisplayVersion,
			Build:          state.FullBuild,
			Architecture:   state.Architecture,
			CollectedAt:    time.Now().Unix(),
			InstalledKbs:   state.InstalledKBs,
		}
		fp := deviceStateFingerprint(rep)
		if fp == lastFingerprint {
			slog.Debug("device-state (Layer 1): unchanged, skipping send", "build", rep.Build)
			return
		}
		payload, err := proto.Marshal(rep)
		if err != nil {
			slog.Error("device-state (Layer 1): marshal failed", "error", err.Error())
			return
		}
		if err := buf.Enqueue(kindDeviceState, payload); err != nil {
			slog.Error("device-state (Layer 1): could not buffer report", "error", err.Error())
			return
		}
		lastFingerprint = fp
		slog.Info("device-state (Layer 1): buffered for sending", "build", rep.Build, "installed_kbs", len(rep.InstalledKbs))
	}

	report() // once at startup, then on the interval (sends only on change)
	ticker := time.NewTicker(cfg.DeviceStateInterval)
	defer ticker.Stop()
	for range ticker.C {
		report()
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
	if bl.Build == "" || bl.Build != state.FullBuild {
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
	return hex.EncodeToString(h.Sum(nil))
}

// runReconciliationScan is Layer 4: the occasional (weekly) heavier offline cab
// scan that reports MISSING updates directly, as a cross-check against the
// server's Layer 1 diff. Uses only the local cab on disk — never the internet.
func runReconciliationScan(cfg config.Config, buf buffer.Buffer) {
	scan := func() {
		slog.Info("reconciliation (Layer 4): starting offline cab scan", "cab", cfg.WsusScanCab)
		updates, err := patch.ScanOffline(cfg.WsusScanCab)
		if err != nil {
			// Fail loud: an errored scan is UNKNOWN, never silently "compliant".
			slog.Error("reconciliation (Layer 4): scan failed (patch status UNKNOWN, not compliant)", "error", err.Error())
			return
		}
		result := buildScanResult(cfg, updates)
		payload, err := proto.Marshal(result)
		if err != nil {
			slog.Error("reconciliation (Layer 4): marshal failed", "error", err.Error())
			return
		}
		if err := buf.Enqueue(kindPatchScanResult, payload); err != nil {
			slog.Error("reconciliation (Layer 4): could not buffer result", "error", err.Error())
			return
		}
		slog.Info("reconciliation (Layer 4): result buffered for sending", "missing", len(result.Missing))
	}

	scan() // once at startup, then on the (weekly) interval
	ticker := time.NewTicker(cfg.ReconScanInterval)
	defer ticker.Stop()
	for range ticker.C {
		scan()
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
			UpdateId:       u.UpdateID,
			KbArticleIds:   u.KBArticleIDs,
			Title:          u.Title,
			Classification: u.Classification,
			Products:       u.Products,
			Type:           u.Type,
			MsrcSeverity:   u.MsrcSeverity,
			CveIds:         u.CveIDs,
			SizeMb:         u.SizeMB,
			RebootRequired: u.RebootRequired,
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
