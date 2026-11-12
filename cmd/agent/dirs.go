package main

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/aegisoneuem/aegis_one_agent/internal/config"
)

// ensureAgentDirs pre-creates the agent's standard directory layout at startup so
// a fresh install shows a complete, professional structure (buffer, cache, certs,
// deferred, download, jobs, logs, tmp) instead of growing folders lazily on first
// use. Best-effort: a failure is logged, not fatal (the owning subsystem still
// MkdirAll's its own dir on use).
func ensureAgentDirs(cfg config.Config) {
	dirs := []string{
		filepath.Join(cfg.SecureDir, "buffer"),
		filepath.Join(cfg.SecureDir, "cache"),
		filepath.Join(cfg.SecureDir, "certs"),
		filepath.Join(cfg.SecureDir, "deferred"),
		filepath.Join(cfg.SecureDir, "jobs"),
		filepath.Join(cfg.SecureDir, "logs"),
		filepath.Join(cfg.SecureDir, "tmp"),
		cfg.DownloadDir,
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			slog.Warn("could not pre-create agent directory", "dir", d, "error", err.Error())
		}
	}
}
