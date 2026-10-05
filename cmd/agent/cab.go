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
	"sync"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// cabManifestHandler tracks which wsusscn2.cab version the agent currently has and
// reacts to the server-reported version: same → nothing; different → fetch the
// new cab via peer-cache over the same mTLS channel (never the internet).
type cabManifestHandler struct {
	versionPath string // SKVC\cab.version — the version of the cab on disk
	cabPath     string // destination path for the cab itself (cfg.WsusScanCab)

	mu           sync.Mutex
	lastNotified string // avoid re-logging the same pending version every check-in
	fetching     bool   // single-flight: one cab fetch at a time
}

func newCabManifestHandler(dir, cabPath string) *cabManifestHandler {
	return &cabManifestHandler{
		versionPath: filepath.Join(dir, "cab.version"),
		cabPath:     cabPath,
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
		if err := fetchContent(ctx, client, req, h.cabPath); err != nil {
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
	}()
}

// fetchContent streams a file from the server (peer-cache delivery) into destPath.
// It writes to a temp file first, verifies the SHA-256 digest (when the request
// carries one), then atomically renames into place. The endpoint never touches the
// internet — content only ever arrives over this mTLS channel.
func fetchContent(ctx context.Context, client pb.AgentControlPlaneClient, req *pb.ContentRequest, destPath string) error {
	stream, err := client.FetchContent(ctx, req)
	if err != nil {
		return fmt.Errorf("FetchContent RPC failed: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("prepare dest dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(destPath), ".fetch-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // best-effort cleanup if we return before the rename

	digest := sha256.New()
	w := io.MultiWriter(tmp, digest)
	var total int64
	for {
		chunk, rerr := stream.Recv()
		if chunk != nil && len(chunk.Data) > 0 {
			n, werr := w.Write(chunk.Data)
			if werr != nil {
				tmp.Close()
				return fmt.Errorf("write temp file: %w", werr)
			}
			total += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			tmp.Close()
			return fmt.Errorf("stream error after %d bytes: %w", total, rerr)
		}
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if want := strings.ToLower(strings.TrimSpace(req.Sha256)); want != "" {
		got := hex.EncodeToString(digest.Sum(nil))
		if got != want {
			return fmt.Errorf("sha256 mismatch: got %s want %s (%d bytes)", got, want, total)
		}
	}

	if err := os.Rename(tmpName, destPath); err != nil {
		return fmt.Errorf("move fetched file into place: %w", err)
	}
	slog.Info("content fetched and verified", "kind", req.Kind, "id", req.Id, "bytes", total, "dest", destPath)
	return nil
}
