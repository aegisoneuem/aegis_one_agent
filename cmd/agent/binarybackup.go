package main

import (
	"io"
	"os"
	"path/filepath"
)

// Binary "last known good" backup + restore. A protected copy of the agent exe
// (plus the version it is for) is kept so the watchdog can restore the exe if an
// admin DELETES it or REPLACES it with a tampered one. Resource-light: the backup is
// refreshed only on a version change; the DELETE check is a cheap os.Stat each tick;
// the REPLACE check (hash compare) runs infrequently (every few minutes).
//
// Version-aware so a LEGIT update is never reverted: the backup tracks the installed
// version; a replace is only "tamper" when the exe's version still matches the backup
// but its bytes differ. (Agent upgrades stop the old service before swapping the exe,
// so the old watchdog isn't running when the new exe lands.)
//
// Honest bound: the backup is on the same box — a determined admin can delete BOTH
// the exe and the backup (Phase-B / server-redeploy boundary). This self-heals the
// common single-file delete/replace.

func backupBinaryPath(secureDir string) string  { return filepath.Join(secureDir, ".img") }
func backupVersionPath(secureDir string) string { return filepath.Join(secureDir, ".img.ver") }

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

// ensureBinaryBackup writes/refreshes the backup when it is missing or the installed
// version changed (a legit update). Cheap: a Stat + a small file read; copies only
// on a version change.
func ensureBinaryBackup(secureDir, version string) {
	exe, err := osExecutable()
	if err != nil {
		return
	}
	bk := backupBinaryPath(secureDir)
	verFile := backupVersionPath(secureDir)
	cur, _ := os.ReadFile(verFile)
	if _, serr := os.Stat(bk); serr == nil && string(cur) == version {
		return // backup present and matches the running version
	}
	if copyFileContents(exe, bk) == nil {
		_ = os.WriteFile(verFile, []byte(version), 0o600)
	}
}

// restoreBinaryIfMissing restores the exe from the backup when the exe file was
// DELETED. Cheap: two os.Stat calls.
func restoreBinaryIfMissing(secureDir string) bool {
	exe, err := osExecutable()
	if err != nil {
		return false
	}
	if _, err := os.Stat(exe); err == nil {
		return false // present
	}
	if _, err := os.Stat(backupBinaryPath(secureDir)); err != nil {
		return false // no backup
	}
	return copyFileContents(backupBinaryPath(secureDir), exe) == nil
}

// restoreBinaryIfTampered restores the exe when it was REPLACED: same version as the
// backup but a different hash. Hashes the exe + backup, so callers run it
// infrequently (every few minutes), not on every tick.
func restoreBinaryIfTampered(secureDir, version string) bool {
	exe, err := osExecutable()
	if err != nil {
		return false
	}
	bk := backupBinaryPath(secureDir)
	bkVer, _ := os.ReadFile(backupVersionPath(secureDir))
	if string(bkVer) != version {
		return false // backup is for a different version (update in flight) — don't fight it
	}
	eh, e1 := fileSHA256(exe)
	bh, e2 := fileSHA256(bk)
	if e1 != nil || e2 != nil || eh == bh {
		return false // can't compare, or identical → fine
	}
	return copyFileContents(bk, exe) == nil // same version, different bytes = replaced
}
