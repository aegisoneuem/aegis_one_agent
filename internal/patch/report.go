package patch

import (
	"encoding/json"
	"log/slog"

	"github.com/aegisoneuem/aegis_one_agent/internal/buffer"
)

// StateReport is the lean, network-free patch signal the agent sends to the
// server: OS identity/build + installed KBs. Rich metadata (CVE, severity,
// classification, cumulative-vs-normal) is deliberately NOT included — it lives
// in the server's catalog and is joined there per device. This mirrors the
// proposed PatchStateReport proto message (wire contract).
type StateReport struct {
	ProductName     string   `json:"product_name"`
	DisplayVersion  string   `json:"display_version"`
	Build           string   `json:"build"`
	Architecture    string   `json:"architecture"`
	InstalledKBs    []string `json:"installed_kbs"`
	CollectedAtUnix int64    `json:"collected_at_unix"`
}

const kindPatchState = "patch_state"

// SendFunc delivers one report to the server (e.g. over the gRPC stream).
type SendFunc func(StateReport) error

// Report routes a collected state by connectivity — Step B of the recurring
// patch-detection job:
//   - online: first drain any buffered backlog (so the server sees history in
//     order), then send the current report; a send failure falls back to the
//     buffer rather than dropping data.
//   - offline: queue the report in the Local Secure Buffer for a later drain.
func Report(state *LocalState, online bool, send SendFunc, buf buffer.Buffer) error {
	rep := state.toReport()

	if !online {
		return enqueue(buf, rep)
	}

	if _, err := buf.Drain(func(kind string, payload []byte) error {
		if kind != kindPatchState {
			return nil // not ours; let this handler drop it
		}
		var r StateReport
		if err := json.Unmarshal(payload, &r); err != nil {
			return nil // drop corrupt record
		}
		return send(r)
	}); err != nil {
		slog.Warn("patch report: draining backlog failed, buffering current", "error", err.Error())
		return enqueue(buf, rep)
	}

	if err := send(rep); err != nil {
		slog.Warn("patch report: send failed, buffering", "error", err.Error())
		return enqueue(buf, rep)
	}
	slog.Info("patch state reported", "build", rep.Build, "installed_kbs", len(rep.InstalledKBs))
	return nil
}

func enqueue(buf buffer.Buffer, rep StateReport) error {
	data, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	if err := buf.Enqueue(kindPatchState, data); err != nil {
		return err
	}
	slog.Info("patch state queued for later sync (offline)", "build", rep.Build)
	return nil
}

func (s *LocalState) toReport() StateReport {
	return StateReport{
		ProductName:     s.ProductName,
		DisplayVersion:  s.DisplayVersion,
		Build:           s.FullBuild,
		Architecture:    s.Architecture,
		InstalledKBs:    s.InstalledKBs,
		CollectedAtUnix: s.CollectedAt.Unix(),
	}
}
