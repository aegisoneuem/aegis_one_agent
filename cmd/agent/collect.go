package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

// Defaults for collect_file safety caps (overridable per command in the spec).
const (
	collectDefaultMaxEach  = 50 << 20  // 50 MiB per file
	collectDefaultMaxTotal = 500 << 20 // 500 MiB per task
	collectChunkSize       = 1 << 20   // 1 MiB upload chunks (bounded memory)
)

// collect_file: upload endpoint-local files (logs, script output, configs) to the
// server ON DEMAND — the reverse of content delivery. The agent never pushes files
// on its own (BigFix/BladeLogic model: the console asks, the agent answers). The
// work runs on its own goroutine: it is read-only (no servicing/install gates) and
// network-bound, so it must not block the receiver or the install workers.
func (d *dispatcher) handleCollectFileCmd(ctx context.Context, client pb.AgentControlPlaneClient, cmd *pb.Command) {
	var spec pb.CollectFileSpec
	if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
		slog.Error("collect_file: invalid payload", "command_id", cmd.CommandId, "error", err.Error())
		send(ctx, d.outbox, buildCommandResultMessage(cmd.CommandId, "failed", "invalid collect payload"))
		return
	}
	if len(spec.Paths) == 0 {
		send(ctx, d.outbox, actionStatusMsg(simpleActionStatus(cmd.CommandId, actionCollectFile, d.cfg.DeviceID, "",
			pb.ActionPhase_ACTION_PHASE_FAILED, "collect_file needs at least one path")))
		return
	}
	slog.Info("collect_file: accepted", "command_id", cmd.CommandId, "paths", len(spec.Paths))
	send(ctx, d.outbox, actionStatusMsg(simpleActionStatus(cmd.CommandId, actionCollectFile, d.cfg.DeviceID, "",
		pb.ActionPhase_ACTION_PHASE_QUEUED, "queued")))

	go func() {
		defer recoverLog("collect-file")
		d.runCollect(ctx, client, cmd.CommandId, &spec)
	}()
}

// runCollect expands the requested paths (globs allowed), uploads each matched file
// via UploadContent with the caps applied, and reports one terminal ActionStatus.
func (d *dispatcher) runCollect(ctx context.Context, client pb.AgentControlPlaneClient, cmdID string, spec *pb.CollectFileSpec) {
	started := time.Now().Unix()
	send(ctx, d.outbox, actionStatusMsg(&pb.ActionStatus{
		ActionId: cmdID, ActionType: actionCollectFile, DeviceId: d.cfg.DeviceID,
		Phase: pb.ActionPhase_ACTION_PHASE_RUNNING, Reason: "collecting files", StartedAt: started,
	}))

	maxEach := spec.MaxBytesEach
	if maxEach <= 0 {
		maxEach = collectDefaultMaxEach
	}
	maxTotal := spec.MaxTotalBytes
	if maxTotal <= 0 {
		maxTotal = collectDefaultMaxTotal
	}

	// Expand globs → concrete files (dedup absolute paths, keep request order).
	var files []string
	seen := map[string]bool{}
	var notes []string
	for _, p := range spec.Paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		matches, err := filepath.Glob(p)
		if err != nil {
			notes = append(notes, fmt.Sprintf("bad pattern %q: %v", p, err))
			continue
		}
		if len(matches) == 0 {
			notes = append(notes, fmt.Sprintf("no match: %s", p))
			continue
		}
		for _, m := range matches {
			if abs, err := filepath.Abs(m); err == nil {
				m = abs
			}
			if info, err := os.Stat(m); err != nil || info.IsDir() {
				continue // directories and vanished files are skipped
			}
			if !seen[m] {
				seen[m] = true
				files = append(files, m)
			}
		}
	}

	if len(files) == 0 {
		detail := strings.Join(notes, "; ")
		slog.Info("collect_file: nothing to collect", "command_id", cmdID, "detail", detail)
		sendStatus(d.outbox, &pb.ActionStatus{
			ActionId: cmdID, ActionType: actionCollectFile, DeviceId: d.cfg.DeviceID,
			Phase: pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE, Reason: "no files matched the requested paths",
			Detail: detail, StartedAt: started, FinishedAt: time.Now().Unix(),
		})
		return
	}

	// Upload each file; duplicate base names get an index prefix so nothing is
	// overwritten server-side.
	usedNames := map[string]int{}
	var uploaded, failed int
	var totalBytes int64
	for _, path := range files {
		if totalBytes >= maxTotal {
			notes = append(notes, fmt.Sprintf("task cap %d MB reached — remaining files skipped", maxTotal>>20))
			break
		}
		name := filepath.Base(path)
		if n := usedNames[name]; n > 0 {
			name = fmt.Sprintf("%d_%s", n+1, name)
		}
		usedNames[filepath.Base(path)]++

		n, stored, err := uploadFile(ctx, client, cmdID, name, path, spec.TailBytes, maxEach)
		if err != nil {
			failed++
			notes = append(notes, fmt.Sprintf("%s: %v", path, err))
			slog.Warn("collect_file: upload failed", "command_id", cmdID, "file", path, "error", err.Error())
			continue
		}
		uploaded++
		totalBytes += n
		notes = append(notes, fmt.Sprintf("%s (%d bytes) -> %s", path, n, stored))
		slog.Info("collect_file: uploaded", "command_id", cmdID, "file", path, "bytes", n, "stored", stored)
	}

	phase := pb.ActionPhase_ACTION_PHASE_SUCCESS
	reason := fmt.Sprintf("collected %d file(s), %d bytes", uploaded, totalBytes)
	if failed > 0 {
		phase = pb.ActionPhase_ACTION_PHASE_FAILED
		reason = fmt.Sprintf("collected %d of %d file(s) (%d failed)", uploaded, uploaded+failed, failed)
	}
	detail := strings.Join(notes, "\n")
	if len(detail) > 2000 {
		detail = detail[:2000] + "..."
	}
	sendStatus(d.outbox, &pb.ActionStatus{
		ActionId: cmdID, ActionType: actionCollectFile, DeviceId: d.cfg.DeviceID,
		Phase: phase, Reason: reason, Detail: detail,
		StartedAt: started, FinishedAt: time.Now().Unix(),
	})
	slog.Info("collect_file: finished", "command_id", cmdID, "phase", phase.String(), "uploaded", uploaded, "failed", failed, "bytes", totalBytes)
}

// uploadFile streams one file (or its tail, per the caps) to the server over the
// mTLS channel in bounded 1 MiB chunks, with an end-to-end SHA-256 the server
// verifies. Returns the bytes uploaded and the server's stored path.
func uploadFile(ctx context.Context, client pb.AgentControlPlaneClient, actionID, name, path string, tailBytes, maxEach int64) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, "", err
	}

	// Window selection: whole file by default; tail_bytes narrows to the last N;
	// the per-file cap narrows further (keeping the END — the newest log lines).
	size := info.Size()
	length := size
	if tailBytes > 0 && length > tailBytes {
		length = tailBytes
	}
	if length > maxEach {
		length = maxEach
	}
	if start := size - length; start > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return 0, "", err
		}
	}

	stream, err := client.UploadContent(ctx)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	buf := make([]byte, collectChunkSize)
	var sent int64
	first := true
	for sent < length {
		want := length - sent
		if want > int64(len(buf)) {
			want = int64(len(buf))
		}
		n, rerr := io.ReadFull(f, buf[:want])
		if n > 0 {
			h.Write(buf[:n])
			chunk := &pb.UploadChunk{Data: buf[:n]}
			if first {
				chunk.ActionId, chunk.Name, chunk.TotalSize = actionID, name, length
				first = false
			}
			if err := stream.Send(chunk); err != nil {
				return sent, "", fmt.Errorf("send: %w", err)
			}
			sent += int64(n)
		}
		if rerr != nil {
			break // short read: file shrank mid-upload — send what we have
		}
	}
	// Final chunk carries the end-to-end digest (header fields repeated for a
	// zero-byte file where this is also the first chunk).
	final := &pb.UploadChunk{Sha256Full: hex.EncodeToString(h.Sum(nil))}
	if first {
		final.ActionId, final.Name, final.TotalSize = actionID, name, 0
	}
	if err := stream.Send(final); err != nil {
		return sent, "", fmt.Errorf("send final: %w", err)
	}
	ack, err := stream.CloseAndRecv()
	if err != nil {
		return sent, "", fmt.Errorf("server ack: %w", err)
	}
	if ack.Error != "" {
		return sent, "", fmt.Errorf("server refused: %s", ack.Error)
	}
	if !ack.IntegrityOk {
		return sent, "", fmt.Errorf("server integrity check failed (sent %d bytes)", sent)
	}
	return sent, ack.StoredPath, nil
}
