package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// cabManifestHandler tracks which wsusscn2.cab version the agent currently has and
// reacts to the server-reported version: same → nothing; different → fetch the
// new cab via peer-cache.
//
// NOTE: the actual fetch is peer-cache delivery, which is not built yet (and is
// partly server-track). For now fetchStale logs that a fetch is needed; once
// peer-cache exists it will pull + verify the cab and call setLocalVersion.
type cabManifestHandler struct {
	versionPath  string // SKVC\cab.version — the version of the cab on disk
	mu           sync.Mutex
	lastNotified string // avoid re-logging the same pending version every check-in
}

func newCabManifestHandler(dir string) *cabManifestHandler {
	return &cabManifestHandler{versionPath: filepath.Join(dir, "cab.version")}
}

func (h *cabManifestHandler) localVersion() string {
	data, err := os.ReadFile(h.versionPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// setLocalVersion records the version of the cab now on disk (called after a
// successful fetch — wired when peer-cache lands).
func (h *cabManifestHandler) setLocalVersion(v string) error {
	tmp := h.versionPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(v), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, h.versionPath)
}

// onManifest compares the server's cab version with the local one and triggers a
// fetch when stale (deduped per version so a steady server does not log repeatedly).
func (h *cabManifestHandler) onManifest(serverVersion, sha256 string) {
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
	h.fetchStale(serverVersion, sha256)
}

// fetchStale will pull the new cab via peer-cache and verify its SHA-256. Until
// peer-cache is built it is a no-op that records the gap.
func (h *cabManifestHandler) fetchStale(version, sha256 string) {
	slog.Warn("cab fetch pending: peer-cache delivery not yet implemented",
		"needed_version", version, "expected_sha256", sha256)
	// TODO(peer-cache): fetch cab -> verify sha256 -> h.setLocalVersion(version).
}
