package main

import (
	"io"
	"os"
	"path/filepath"
)

// Binary "last known good" backup + restore. A protected copy of the agent exe is
// kept so that if an admin DELETES the running exe, the watchdog restores it (a
// subsequent service restart then runs a good binary). Resource-light: the backup is
// written once; the watchdog check is a cheap os.Stat (no hashing in the loop).
//
// Honest bound: the backup lives on the same box, so a determined admin can delete
// BOTH the exe and the backup — that is the Phase-B / server-redeploy boundary. This
// self-heals the common single-file-delete case.

// backupBinaryPath is an innocuous name (not "agent.bak") alongside the data dir.
func backupBinaryPath(secureDir string) string { return filepath.Join(secureDir, ".img") }

func copyFileContents(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	out.Close()
	return os.Rename(tmp, dst)
}

// ensureBinaryBackup writes the backup copy once (if absent).
func ensureBinaryBackup(secureDir string) {
	exe, err := osExecutable()
	if err != nil {
		return
	}
	bk := backupBinaryPath(secureDir)
	if _, err := os.Stat(bk); err == nil {
		return // already backed up
	}
	_ = copyFileContents(exe, bk) // best-effort
}

// restoreBinaryIfMissing restores the exe from the backup when the exe file has been
// deleted. Returns true if it restored. Cheap: two os.Stat calls.
func restoreBinaryIfMissing(secureDir string) bool {
	exe, err := osExecutable()
	if err != nil {
		return false
	}
	if _, err := os.Stat(exe); err == nil {
		return false // exe present
	}
	bk := backupBinaryPath(secureDir)
	if _, err := os.Stat(bk); err != nil {
		return false // no backup to restore from
	}
	return copyFileContents(bk, exe) == nil
}
