//go:build windows

package main

import (
	"log/slog"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/buffer"
	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	"github.com/aegisoneuem/aegis_one_agent/internal/patch"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

// runPatchDetection runs the local offline patch scan on an interval and writes
// each result to the buffer — never touching the network. The connect loop
// drains the buffer and sends when connected to the server. Disabled when no cab
// is configured (the server provides the cab).
func runPatchDetection(cfg config.Config, buf buffer.Buffer) {
	if cfg.WsusScanCab == "" {
		slog.Info("patch detection disabled (no AEGIS_WSUS_SCAN_CAB configured)")
		return
	}

	scan := func() {
		slog.Info("patch detection: starting local offline scan", "cab", cfg.WsusScanCab)
		updates, err := patch.ScanOffline(cfg.WsusScanCab)
		if err != nil {
			// Fail loud: an errored scan is UNKNOWN, never silently "compliant".
			slog.Error("patch detection: scan failed (patch status UNKNOWN, not compliant)", "error", err.Error())
			return
		}
		result := buildScanResult(cfg, updates)
		payload, err := proto.Marshal(result)
		if err != nil {
			slog.Error("patch detection: marshal failed", "error", err.Error())
			return
		}
		if err := buf.Enqueue(kindPatchScanResult, payload); err != nil {
			slog.Error("patch detection: could not buffer result", "error", err.Error())
			return
		}
		slog.Info("patch detection: result buffered for sending", "missing", len(result.Missing))
	}

	scan() // run once at startup, then on the interval
	ticker := time.NewTicker(cfg.PatchScanInterval)
	defer ticker.Stop()
	for range ticker.C {
		scan()
	}
}

func buildScanResult(cfg config.Config, updates []patch.Update) *pb.PatchScanResult {
	state, err := patch.CollectLocalState()
	if err != nil {
		slog.Warn("patch detection: could not read local state for report", "error", err.Error())
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
