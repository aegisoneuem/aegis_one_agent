package main

import (
	"log/slog"
	"path/filepath"
)

// Tamper-protection Phase A, Step 1: install-directory ACL hardening.
//
// hardenInstallTree locks down the agent's install tree so a NON-admin user can
// neither modify nor delete the agent's files (SYSTEM + Administrators full
// control; Users read + execute only). It RE-APPLIES on every startup, so if an
// admin loosens the ACL the agent self-heals it on the next restart.
//
// Honest bound: an admin can still rewrite the ACL (take-ownership); that case is
// covered by service self-protection + watchdog self-heal (later steps) and is
// ultimately a Phase-B (kernel driver) concern. This step fully blocks NON-admin
// tampering and raises the bar for admins.
func hardenInstallTree() {
	exe, err := osExecutable()
	if err != nil {
		slog.Warn("tamper-protect: cannot resolve executable for ACL hardening", "error", err.Error())
		return
	}
	// exe = ...\AegisOne\Client\SKVC\AegisOneClient.exe -> harden ...\AegisOne\Client
	clientDir := filepath.Dir(filepath.Dir(exe))
	if clientDir == "" || clientDir == "." {
		return
	}
	if err := hardenDirACL(clientDir); err != nil {
		slog.Warn("tamper-protect: install-dir ACL hardening failed", "dir", clientDir, "error", err.Error())
		return
	}
	slog.Info("tamper-protect: install-dir ACL hardened (SYSTEM+Admins full, Users read-only)", "dir", clientDir)
}
