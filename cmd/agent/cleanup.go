package main

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
)

// sweepTempDir removes every leftover file in the agent's transient temp dir at
// startup. The temp dir only ever holds per-operation plaintext files
// (stage-*/install-*/.fetch-*.tmp) that the stager and installer delete inline
// when an operation finishes normally. Anything still here at startup is the
// residue of an operation that was interrupted (crash, kill, or service
// restart mid-install), so it is safe — and desirable — to clear: it reclaims
// disk and removes stray plaintext patch files.
//
// This runs BEFORE the install/stage workers start, so it never races an
// in-flight operation. It is best-effort and never fatal: cleanup failing must
// not stop the agent from coming up.
func sweepTempDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("startup cleanup: could not read temp dir", "dir", dir, "error", err.Error())
		}
		return
	}

	var files, failed int
	var bytes int64
	for _, e := range entries {
		if e.IsDir() {
			continue // temp dir is flat; leave any unexpected subdir for an admin to inspect
		}
		path := filepath.Join(dir, e.Name())
		if info, statErr := os.Stat(path); statErr == nil {
			bytes += info.Size()
		}
		// SecureDelete: these may be plaintext patch files, so overwrite-then-remove.
		if err := secure.SecureDelete(path); err != nil {
			failed++
			slog.Warn("startup cleanup: could not remove leftover temp", "file", path, "error", err.Error())
			continue
		}
		files++
	}

	if files > 0 || failed > 0 {
		slog.Info("startup cleanup: cleared interrupted-operation temp files",
			"dir", dir, "removed", files, "bytes", bytes, "failed", failed)
	}
}
