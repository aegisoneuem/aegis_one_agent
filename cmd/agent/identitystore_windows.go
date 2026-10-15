//go:build windows

package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

// The durable identity store lives in the HKLM registry, NOT under the install
// dir (SKVC). An agent uninstall/reinstall typically removes the install tree
// (and with it SKVC\identity.json), which would otherwise force a fresh device_id
// + a re-enroll that gets a new computer_id. HKLM survives that, so the device
// keeps its identity across reinstall without even needing a server round-trip.
// Writing HKLM needs elevation; the agent runs as LocalSystem, so it can.
const (
	identityRegPath  = `SOFTWARE\AegisOne`
	identityRegValue = "identity"
)

// readDurableIdentity reads the identity record from the durable machine store.
// ok=false when none is stored or it cannot be read/parsed.
func readDurableIdentity() (identity, bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, identityRegPath, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return identity{}, false
	}
	defer k.Close()
	s, _, err := k.GetStringValue(identityRegValue)
	if err != nil {
		return identity{}, false
	}
	var id identity
	if json.Unmarshal([]byte(s), &id) != nil || id.DeviceID == "" {
		return identity{}, false
	}
	return id, true
}

// writeDurableIdentity mirrors the identity record to the durable machine store.
// Best-effort: a non-elevated dev run cannot write HKLM (logged at debug) — the
// LocalSystem service can.
func writeDurableIdentity(id identity) {
	data, err := json.Marshal(id)
	if err != nil {
		return
	}
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, identityRegPath, registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		slog.Debug("identity: could not open durable store for write (needs elevation?)", "error", err.Error())
		return
	}
	defer k.Close()
	if err := k.SetStringValue(identityRegValue, string(data)); err != nil {
		slog.Debug("identity: could not write durable store", "error", err.Error())
	}
}

// restoreIdentityIfMissing recreates SKVC\identity.json from the durable machine
// store when the file is absent (e.g. an uninstall/reinstall wiped the install
// dir). resolveDeviceID then reads it normally and — because the hardware signals
// still match — reuses the SAME device_id + computer_id instead of regenerating.
func restoreIdentityIfMissing(secureDir string) {
	path := filepath.Join(secureDir, "identity.json")
	if _, err := os.Stat(path); err == nil {
		return // file present → nothing to restore
	}
	id, ok := readDurableIdentity()
	if !ok {
		return
	}
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(secureDir, 0o755); err != nil {
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		slog.Warn("identity: could not restore identity.json from durable store", "error", err.Error())
		return
	}
	slog.Info("identity: restored from durable machine store (survived reinstall)",
		"device_id", id.DeviceID, "computer_id", id.ComputerID)
}

// mirrorIdentityToDurable copies the current SKVC\identity.json into the durable
// machine store so it survives a future uninstall/reinstall. Called after the
// identity is fully resolved (post-enroll), so device_id + computer_id are final.
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
