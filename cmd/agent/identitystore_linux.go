//go:build linux

package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
)

// Linux durable identity store. The data dir (/var/opt/aegisone) is removed on an
// uninstall/reinstall, which would force a fresh device_id + re-enroll (new
// computer_id). So the identity is mirrored to /var/lib/aegisone/identity.json —
// created by the AGENT at runtime, NOT owned by the package, so a package removal
// leaves it intact and reinstall recovers the same ids. Root-only (0600).
//
// (Windows uses the HKLM registry for the same purpose.) Not admin-tamper-proof — the
// TPM-NV-sealed identity is the planned admin-proof anchor; identity is never a trust
// anchor anyway (authorization is the Ed25519 cert/signature).

const durableIdentityPath = "/var/lib/aegisone/identity.json"

func readDurableIdentity() (identity, bool) {
	data, err := os.ReadFile(durableIdentityPath)
	if err != nil {
		return identity{}, false
	}
	var id identity
	if json.Unmarshal(data, &id) != nil || id.DeviceID == "" {
		return identity{}, false
	}
	return id, true
}

func writeDurableIdentity(id identity) {
	data, err := json.Marshal(id)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(durableIdentityPath), 0o700); err != nil {
		slog.Debug("identity: could not create durable dir (run as root?)", "error", err.Error())
		return
	}
	if err := os.WriteFile(durableIdentityPath, data, 0o600); err != nil {
		slog.Debug("identity: could not write durable store", "error", err.Error())
	}
}

// restoreIdentityIfMissing recreates <dataDir>/identity.json from the durable store
// when it is absent (e.g. an uninstall/reinstall wiped the data dir), so the device
// keeps its device_id + computer_id.
func restoreIdentityIfMissing(secureDir string) {
	path := filepath.Join(secureDir, "identity.json")
	if _, err := os.Stat(path); err == nil {
		return
	}
	id, ok := readDurableIdentity()
	if !ok {
		return
	}
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(secureDir, 0o700); err != nil {
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		slog.Warn("identity: could not restore identity.json from durable store", "error", err.Error())
		return
	}
	slog.Info("identity: restored from durable store (survived reinstall)",
		"device_id", id.DeviceID, "computer_id", id.ComputerID)
}

// mirrorIdentityToDurable copies <dataDir>/identity.json into the durable store so it
// survives a future uninstall/reinstall. Called after the identity is fully resolved.
func mirrorIdentityToDurable(secureDir string) {
	data, err := os.ReadFile(filepath.Join(secureDir, "identity.json"))
	if err != nil {
		return
	}
	var id identity
	if json.Unmarshal(data, &id) != nil || id.DeviceID == "" {
		return
	}
	writeDurableIdentity(id)
}
