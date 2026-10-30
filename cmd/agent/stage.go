package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aegisoneuem/aegis_one_agent/internal/patchcache"
	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// stager pre-caches ("stages") patches ahead of a scheduled install. On a signed
// stage_patch command it fetches the patch from the server via FetchContent
// (peer-cache, no internet), encrypts it into the patch cache as StateStaged
// (evict- and delete-proof until its deployment completes), and reports the
// outcome. A later install_patch for the same KB finds it already staged and
// installs straight from the cache — no second fetch.
type stager struct {
	outbox   chan<- *pb.AgentMessage
	cache    *patchcache.Cache
	tmpDir   string
	deviceID string
	fetchBPS int64 // agent-config fetch cap (0 = unlimited)

	mu       sync.Mutex
	inflight map[string]bool // single-flight per cache id
}

func newStager(outbox chan<- *pb.AgentMessage, cache *patchcache.Cache, tmpDir, deviceID string, fetchBPS int64) *stager {
	return &stager{outbox: outbox, cache: cache, tmpDir: tmpDir, deviceID: deviceID, fetchBPS: fetchBPS, inflight: make(map[string]bool)}
}

// stage handles one stage_patch command. The fetch+encrypt runs in a background
// goroutine (single-flight per id) so it never blocks the receiver — a patch can
// be large and its download can take a while.
func (s *stager) stage(ctx context.Context, client pb.AgentControlPlaneClient, cmdID string, spec *pb.InstallPatchSpec) {
	id := cacheID(spec)
	st := func(phase pb.ActionPhase, reason string) *pb.AgentMessage {
		return actionStatusMsg(simpleActionStatus(cmdID, actionStage, s.deviceID, spec.Kb, phase, reason))
	}
	if id == "" {
		send(ctx, s.outbox, st(pb.ActionPhase_ACTION_PHASE_FAILED, "stage_patch: missing kb/hash identifier"))
		return
	}
	if s.cache == nil {
		send(ctx, s.outbox, st(pb.ActionPhase_ACTION_PHASE_FAILED, "stage_patch: cache unavailable"))
		return
	}
	if s.cache.Has(id) {
		slog.Info("stage_patch: already staged", "command_id", cmdID, "id", id)
		send(ctx, s.outbox, st(pb.ActionPhase_ACTION_PHASE_SUCCESS, "already staged"))
		return
	}

	s.mu.Lock()
	if s.inflight[id] {
		s.mu.Unlock()
		slog.Info("stage_patch: already staging, ignoring duplicate", "command_id", cmdID, "id", id)
		send(ctx, s.outbox, st(pb.ActionPhase_ACTION_PHASE_RUNNING, "stage already in progress"))
		return
	}
	s.inflight[id] = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.inflight, id)
			s.mu.Unlock()
		}()
		defer recoverLog("stage-patch")

		send(ctx, s.outbox, st(pb.ActionPhase_ACTION_PHASE_DOWNLOADING, "fetching patch for staging"))

		// The extension (.msu/.cab) comes from the server-provided filename so the
		// cached file keeps the extension DISM expects at install time.
		ext := filepath.Ext(strings.TrimSpace(spec.FilePath))
		if ext == "" {
			ext = ".msu"
		}
		tmp := filepath.Join(s.tmpDir, "stage-"+sanitize(cmdID)+ext)

		req := &pb.ContentRequest{Kind: "patch", Id: id, Sha256: spec.ExpectedHash}
		slog.Info("stage_patch: fetching patch from server", "command_id", cmdID, "id", id, "dest", tmp)
		// stage_patch does not expose a per-task rate yet; only the agent
		// config cap applies here. 0 = no cap.
		if err := fetchContent(ctx, client, req, tmp, s.fetchBPS); err != nil {
			slog.Error("stage_patch: fetch failed", "command_id", cmdID, "id", id, "error", err.Error())
			send(ctx, s.outbox, st(pb.ActionPhase_ACTION_PHASE_FAILED, "fetch failed: "+err.Error()))
			return
		}

		// Size the plaintext before it is encrypted into the cache (used in the
		// cache-full report); best-effort.
		var needed int64
		if info, err := os.Stat(tmp); err == nil {
			needed = info.Size()
		}

		err := s.cache.Put(id, tmp, patchcache.StateStaged)
		// The plaintext temp is always removed — the cache holds the encrypted copy.
		if rmErr := secure.SecureDelete(tmp); rmErr != nil {
			slog.Warn("stage_patch: could not remove temp", "temp", tmp, "error", rmErr.Error())
		}
		if err != nil {
			if errors.Is(err, patchcache.ErrCacheFull) {
				slog.Warn("stage_patch: cache full, cannot stage", "command_id", cmdID, "id", id, "error", err.Error())
				s.reportCacheFull(ctx, id, needed)
				send(ctx, s.outbox, st(pb.ActionPhase_ACTION_PHASE_FAILED, "cache full: "+err.Error()))
				return
			}
			slog.Error("stage_patch: cache put failed", "command_id", cmdID, "id", id, "error", err.Error())
			send(ctx, s.outbox, st(pb.ActionPhase_ACTION_PHASE_FAILED, "cache put failed: "+err.Error()))
			return
		}
		slog.Info("stage_patch: patch staged in cache", "command_id", cmdID, "id", id, "bytes", needed)
		send(ctx, s.outbox, st(pb.ActionPhase_ACTION_PHASE_SUCCESS, "patch staged in cache"))
	}()
}

// reportCacheFull tells the server the cache is full so it can raise the cap or
// trigger the cleanup task — fail-loud, never a silent drop.
func (s *stager) reportCacheFull(ctx context.Context, pendingID string, needed int64) {
	inUse, capBytes := s.cache.Usage()
	send(ctx, s.outbox, &pb.AgentMessage{Payload: &pb.AgentMessage_CacheFull{CacheFull: &pb.CacheFullReport{
		DeviceId:    s.deviceID,
		PendingId:   pendingID,
		NeededBytes: needed,
		CapBytes:    capBytes,
		InUseBytes:  inUse,
		Message:     "patch cache full — increase the cache cap or run the client cache-cleanup task",
	}}})
}
