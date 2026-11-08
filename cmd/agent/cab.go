package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/throttle"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// cabManifestHandler tracks which wsusscn2.cab version the agent currently has and
// reacts to the server-reported version: same → nothing; different → fetch the
// new cab via peer-cache over the same mTLS channel (never the internet).
type cabManifestHandler struct {
	versionPath string        // SKVC\cab.version — the version of the cab on disk
	cabPath     string        // destination path for the cab itself (cfg.WsusScanCab)
	scanTrigger chan<- struct{} // signalled after a new cab lands, to run a full compliance scan
	fetchBPS    int64         // agent-config fetch bytes-per-second cap (0 = unlimited)

	mu           sync.Mutex
	lastNotified string // avoid re-logging the same pending version every check-in
	fetching     bool   // single-flight: one cab fetch at a time
}

func newCabManifestHandler(dir, cabPath string, scanTrigger chan<- struct{}, fetchBPS int64) *cabManifestHandler {
	return &cabManifestHandler{
		versionPath: filepath.Join(dir, "cab.version"),
		cabPath:     cabPath,
		scanTrigger: scanTrigger,
		fetchBPS:    fetchBPS,
	}
}

func (h *cabManifestHandler) localVersion() string {
	data, err := os.ReadFile(h.versionPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// setLocalVersion records the version of the cab now on disk (called after a
// successful, hash-verified fetch).
func (h *cabManifestHandler) setLocalVersion(v string) error {
	tmp := h.versionPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(v), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, h.versionPath)
}

// onManifest compares the server's cab version with the local one and triggers a
// fetch when stale (deduped per version so a steady server does not log repeatedly).
func (h *cabManifestHandler) onManifest(ctx context.Context, client pb.AgentControlPlaneClient, serverVersion, sha256 string) {
	serverVersion = strings.TrimSpace(serverVersion)
	if serverVersion == "" {
		return
	}
	local := h.localVersion()
	if serverVersion == local {
		slog.Debug("cab up to date", "version", local)
		return
	}
	h.mu.Lock()
	already := h.lastNotified == serverVersion
	h.lastNotified = serverVersion
	h.mu.Unlock()
	if already {
		return
	}
	slog.Info("new wsusscn2.cab available from server", "server_version", serverVersion, "local_version", local)
	h.fetchStale(ctx, client, serverVersion, sha256)
}

// fetchStale pulls the new cab via peer-cache (FetchContent), verifies its
// SHA-256, writes it into place, and records the new version. It runs in a
// background goroutine (single-flight) so it never blocks the receiver — DISM/cab
// files are large. A failure resets the dedup guard so the next manifest retries.
func (h *cabManifestHandler) fetchStale(ctx context.Context, client pb.AgentControlPlaneClient, version, sha256 string) {
	if h.cabPath == "" {
		slog.Warn("cannot fetch cab: no local cab path configured (AEGIS_WSUS_SCAN_CAB)")
		return
	}
	h.mu.Lock()
	if h.fetching {
		h.mu.Unlock()
		return // a fetch is already in flight
	}
	h.fetching = true
	h.mu.Unlock()

	go func() {
		defer func() {
			h.mu.Lock()
			h.fetching = false
			h.mu.Unlock()
		}()
		defer recoverLog("cab-fetch")

		slog.Info("fetching wsusscn2.cab from server", "version", version, "dest", h.cabPath)
		req := &pb.ContentRequest{Kind: "cab", Id: version, Sha256: sha256}
		// Cab fetches do not carry a signed per-task rate (control-plane
		// catalog delivery, not a software deploy), so only the agent config
		// cap applies here. 0 = no cap.
		if err := fetchContent(ctx, client, req, h.cabPath, h.fetchBPS); err != nil {
			slog.Error("cab fetch failed", "version", version, "error", err.Error())
			// Allow a retry on the next manifest for this same version.
			h.mu.Lock()
			if h.lastNotified == version {
				h.lastNotified = ""
			}
			h.mu.Unlock()
			return
		}
		if err := h.setLocalVersion(version); err != nil {
			slog.Error("cab fetched but could not record version", "version", version, "error", err.Error())
			return
		}
		slog.Info("wsusscn2.cab updated", "version", version, "path", h.cabPath)
		// New catalog on disk → trigger a full compliance scan now (don't wait for
		// the weekly tick). A new/uninstalled patch WITHOUT a cab change is caught
		// cheaply by the fast lane; only a changed catalog needs the heavy scan.
		signalDrain(h.scanTrigger)
	}()
}

// fetch tuning: how many times a single fetchContent call will resume an
// interrupted transfer before giving up, and the pause between attempts. These
// are vars (not consts) only so tests can shorten the backoff.
var (
	fetchMaxAttempts  = 5
	fetchRetryBackoff = 2 * time.Second
)

// errFetchCorruptPartial means a transfer COMPLETED but the whole-file SHA-256
// did not match. The bytes on disk are bad, so the partial cannot be resumed —
// it is discarded and the transfer restarts from zero. This is distinct from a
// mid-transfer disconnect, which keeps the partial and resumes from its offset.
var errFetchCorruptPartial = errors.New("fetched content failed integrity check")

// errFetchResetPartial means the resume offset was rejected by the server
// (the partial on disk is larger than the source, e.g. a stale leftover). The
// partial is discarded and the transfer restarts cleanly from zero.
var errFetchResetPartial = errors.New("resume offset rejected; restarting from zero")

// fetchContent streams a file from the server (peer-cache delivery) into
// destPath, with CHECKPOINT RESTART: a mid-transfer disconnect keeps the bytes
// already written and resumes from that byte offset instead of restarting from
// zero, so a large package is not re-sent on every network blip. The completed
// file's SHA-256 is verified before it is renamed into place, so a resumed
// transfer is exactly as trustworthy as a single-shot one. The endpoint never
// touches the internet — content only ever arrives over this mTLS channel.
//
// bytesPerSecond caps the receive-side rate for this transfer (0 = no cap). The
// caller resolves the effective cap via throttle.EffectiveRate(agent_config,
// signed_command) so BigFix "lower wins" between admin cap and per-task rate
// is applied before this function sees a number.
func fetchContent(ctx context.Context, client pb.AgentControlPlaneClient, req *pb.ContentRequest, destPath string, bytesPerSecond int64) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("prepare dest dir: %w", err)
	}
	// Content-addressed partial file: it survives across retry attempts so an
	// interrupted transfer resumes from the bytes already on disk. Keyed by the
	// pinned SHA (falls back to id) rather than the dest path.
	partialPath := fetchPartialPath(destPath, req)
	// Resume is only trusted when the transfer is integrity-pinned: a persisted
	// partial is bytes-on-disk we did not verify this run, so we only reuse it when
	// the whole-file SHA-256 will be checked before the file is accepted. An
	// unpinned transfer never resumes — it starts fresh each attempt (see
	// fetchAttempt) and never keeps a partial on exit.
	resumable := strings.TrimSpace(req.Sha256) != ""
	if resumable {
		if info, serr := os.Stat(partialPath); serr == nil && info.Size() > 0 {
			slog.Info("fetch: found a partial from an earlier interrupted transfer — will resume",
				"kind", req.Kind, "id", req.Id, "have_bytes", info.Size(), "partial", partialPath)
		}
	}

	var lastErr error
	for attempt := 1; attempt <= fetchMaxAttempts; attempt++ {
		err := fetchAttempt(ctx, client, req, partialPath, destPath, bytesPerSecond)
		if err == nil {
			return nil
		}
		lastErr = err
		if ctx.Err() != nil {
			// Shutdown / connection drop — stop retrying on THIS (now-dead) client.
			// KEEP a non-empty partial so when the server re-issues the command on
			// reconnect, the fresh fetch resumes from these bytes instead of zero
			// (BigFix-style checkpoint restart across a reconnect). Only for pinned
			// transfers — an unpinned partial would be reused without verification,
			// so it is never kept. An empty partial has nothing to resume either.
			if info, serr := os.Stat(partialPath); !resumable || serr != nil || info.Size() == 0 {
				_ = os.Remove(partialPath)
			} else {
				slog.Info("fetch: connection lost mid-transfer; partial kept for resume on re-issue",
					"kind", req.Kind, "id", req.Id, "have_bytes", info.Size())
			}
			return err
		}
		if errors.Is(err, errFetchCorruptPartial) || errors.Is(err, errFetchResetPartial) {
			// Either the completed bytes are bad (SHA mismatch) or the resume offset
			// was past the source end (stale partial): resume cannot fix it. Discard
			// and restart clean from zero.
			slog.Warn("fetch: discarding partial and restarting from zero",
				"kind", req.Kind, "id", req.Id, "attempt", attempt, "error", err.Error())
			_ = os.Remove(partialPath)
		} else {
			have := int64(0)
			if info, serr := os.Stat(partialPath); serr == nil {
				have = info.Size()
			}
			slog.Warn("fetch: transfer interrupted, will resume",
				"kind", req.Kind, "id", req.Id, "attempt", attempt, "of", fetchMaxAttempts, "have_bytes", have, "error", err.Error())
		}
		if attempt < fetchMaxAttempts {
			select {
			case <-ctx.Done():
				_ = os.Remove(partialPath)
				return ctx.Err()
			case <-time.After(fetchRetryBackoff):
			}
		}
	}
	_ = os.Remove(partialPath)
	return fmt.Errorf("fetch failed after %d attempts: %w", fetchMaxAttempts, lastErr)
}

// fetchAttempt runs ONE streaming pass. It appends to partialPath starting at
// that file's current size (the resume offset it asks the server for), updates
// a running SHA-256 seeded with the bytes already on disk, and on a clean finish
// verifies the whole-file digest and renames the partial into destPath.
// A returned error that is NOT errFetchCorruptPartial is a resumable interruption
// (the partial is kept); errFetchCorruptPartial means the partial must be dropped.
func fetchAttempt(ctx context.Context, client pb.AgentControlPlaneClient, req *pb.ContentRequest, partialPath, destPath string, bytesPerSecond int64) error {
	// Resume (and therefore trust bytes already on disk) ONLY when the transfer is
	// SHA-pinned, because the whole-file digest is what makes a resumed partial
	// trustworthy. Unpinned transfers start fresh every attempt and truncate any
	// leftover partial, so an unverified file is never reused.
	resumable := strings.TrimSpace(req.Sha256) != ""
	have := int64(0)
	if resumable {
		if info, err := os.Stat(partialPath); err == nil {
			have = info.Size()
		}
	}
	// Re-seed the running digest with the bytes already on disk so the final
	// digest covers the WHOLE file after a resume, not just this pass's bytes.
	digest := sha256.New()
	if have > 0 {
		rf, err := os.Open(partialPath)
		if err != nil {
			return fmt.Errorf("reopen partial for digest: %w", err)
		}
		if _, err := io.Copy(digest, rf); err != nil {
			rf.Close()
			return fmt.Errorf("rehash partial: %w", err)
		}
		rf.Close()
	}

	// Pinned: append so a resume adds to the existing partial. Unpinned: truncate
	// so a stale leftover is never silently appended to or trusted.
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if !resumable {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	f, err := os.OpenFile(partialPath, flags, 0o600)
	if err != nil {
		return fmt.Errorf("open partial: %w", err)
	}

	rreq := &pb.ContentRequest{Kind: req.Kind, Id: req.Id, Sha256: req.Sha256, Offset: have}
	stream, err := client.FetchContent(ctx, rreq)
	if err != nil {
		f.Close()
		return fmt.Errorf("FetchContent RPC failed: %w", err)
	}

	// Throttle the WRITE so TCP backpressure propagates up the gRPC stream to the
	// server (receiver slows -> flow control -> server stops pushing). The digest
	// multiwriter stays off the throttle so pacing is tied to persistent-storage
	// bytes, not buffer churn. A zero rate is a zero-overhead passthrough.
	paced := throttle.NewWriter(f, bytesPerSecond)
	w := io.MultiWriter(paced, digest)
	total := have
	for {
		chunk, rerr := stream.Recv()
		if chunk != nil && len(chunk.Data) > 0 {
			n, werr := w.Write(chunk.Data)
			if werr != nil {
				f.Close()
				return fmt.Errorf("write partial: %w", werr)
			}
			total += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			// A rejected resume offset (stale partial larger than the source) is not
			// a transient disconnect — it must restart from zero, not keep resuming
			// the same bad offset forever.
			if status.Code(rerr) == codes.OutOfRange {
				return fmt.Errorf("%w: %v", errFetchResetPartial, rerr)
			}
			return fmt.Errorf("stream error after %d bytes: %w", total, rerr)
		}
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close partial: %w", err)
	}

	if want := strings.ToLower(strings.TrimSpace(req.Sha256)); want != "" {
		got := hex.EncodeToString(digest.Sum(nil))
		if got != want {
			return fmt.Errorf("%w: got %s want %s (%d bytes)", errFetchCorruptPartial, got, want, total)
		}
	}

	if err := os.Rename(partialPath, destPath); err != nil {
		return fmt.Errorf("move fetched file into place: %w", err)
	}
	slog.Info("content fetched and verified", "kind", req.Kind, "id", req.Id, "bytes", total, "resumed_from", have, "dest", destPath)
	return nil
}

// fetchPartialPath returns the content-addressed partial file path next to the
// destination. Keyed by the pinned SHA (hex, filesystem-safe) when present, else
// the id, so the same content resumes even across separate fetchContent calls.
func fetchPartialPath(destPath string, req *pb.ContentRequest) string {
	key := strings.TrimSpace(req.Sha256)
	if key == "" {
		key = strings.TrimSpace(req.Id)
	}
	key = sanitizeFetchKey(key)
	if key == "" {
		key = "content"
	}
	if len(key) > 32 {
		key = key[:32]
	}
	return filepath.Join(filepath.Dir(destPath), ".fetch-"+key+".partial")
}

// sanitizeFetchKey keeps only characters safe for a file name.
func sanitizeFetchKey(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, s)
}
