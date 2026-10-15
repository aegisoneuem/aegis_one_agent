package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// identity is the persisted device identity record (SKVC\identity.json). It binds
// the stable device id to the hardware signals it was created under, so a
// clone/reimage (ALL signals changed) can be detected and a fresh id issued, while
// adding a new signal (e.g. TPM to an already-enrolled agent) is NOT mistaken for
// a hardware change.
type identity struct {
	DeviceID    string   `json:"device_id"`
	Fingerprint string   `json:"fingerprint"`           // primary (strongest) signal — kept for back-compat
	Signals     []string `json:"signals,omitempty"`     // ALL hardware signals (tpm/smbios/machineguid)
	ComputerID  string   `json:"computer_id,omitempty"` // server-assigned numeric console id (echo)
	CreatedAt   int64    `json:"created_at"`
}

// signalSet is the union of the record's Signals and its legacy Fingerprint.
func (i identity) signalSet() []string {
	set := map[string]struct{}{}
	for _, s := range i.Signals {
		if s = strings.TrimSpace(s); s != "" {
			set[s] = struct{}{}
		}
	}
	if f := strings.TrimSpace(i.Fingerprint); f != "" {
		set[f] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return out
}

// resolveDeviceID returns the agent's stable unique device id and where it came
// from. Precedence:
//  1. explicit override (AEGIS_DEVICE_ID) — testing or a server-pinned id;
//  2. persisted identity that shares ANY hardware signal with this machine → reuse;
//  3. persisted identity sharing NO signal → clone/reimage → new id;
//  4. no identity yet → generate a new id.
//
// signals is the current hardware signal set (best first; may be empty if none
// could be read — then a persisted id is reused as-is, no clone detection).
func resolveDeviceID(override, secureDir string, signals []string) (id, source string, err error) {
	if v := strings.TrimSpace(override); v != "" {
		return v, "config override", nil
	}
	path := filepath.Join(secureDir, "identity.json")

	if data, rerr := os.ReadFile(path); rerr == nil {
		var cur identity
		if json.Unmarshal(data, &cur) == nil && strings.TrimSpace(cur.DeviceID) != "" {
			persisted := cur.signalSet()
			// Clone/reimage ONLY when we can read signals now, the record has signals,
			// and NONE overlap. Overlap on any signal (e.g. SMBIOS still matches after
			// a TPM signal was added) means the same machine — reuse.
			if len(signals) > 0 && len(persisted) > 0 && !overlaps(signals, persisted) {
				return writeNewIdentity(path, signals, "", "regenerated (hardware changed — clone/reimage)")
			}
			// Same machine: keep id + computer id. ACCUMULATE signals — merge any
			// newly-readable signal into the stored set and NEVER drop a known one
			// (union, never shrink). Hardware signals are stable ids; a signal that
			// reads intermittently (TPM EK can be unreadable at boot; some VMs drop
			// the SMBIOS UUID) must not erode the match anchor, or a later boot where
			// that lone remaining signal fails would look like a clone and regenerate
			// the device id (wiping the computer id with it). With a growing union, a
			// regeneration needs ALL previously-seen signals to stop matching at once
			// (= genuine hardware replacement).
			if len(signals) > 0 {
				merged := unionSignals(signals, persisted)
				if !sameSet(merged, persisted) {
					_ = writeIdentity(path, cur.DeviceID, merged, cur.ComputerID)
				}
			}
			return cur.DeviceID, "persisted", nil
		}
	}
	return writeNewIdentity(path, signals, "", "generated")
}

// unionSignals returns the union of a and b — a's order first, then b's extras,
// de-duplicated and blanks dropped. The persisted signal set only ever grows via
// this: once a hardware signal is seen for a device it is kept, so an
// intermittently-readable signal can never shrink the match anchor.
func unionSignals(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range a {
		add(s)
	}
	for _, s := range b {
		add(s)
	}
	return out
}

// overlaps reports whether a and b share at least one element.
func overlaps(a, b []string) bool {
	set := make(map[string]struct{}, len(b))
	for _, s := range b {
		set[s] = struct{}{}
	}
	for _, s := range a {
		if _, ok := set[s]; ok {
			return true
		}
	}
	return false
}

// sameSet reports whether a and b contain the same elements (order-independent).
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, s := range a {
		set[s] = struct{}{}
	}
	for _, s := range b {
		if _, ok := set[s]; !ok {
			return false
		}
	}
	return true
}

func writeNewIdentity(path string, signals []string, computerID, source string) (string, string, error) {
	id, err := newUUID()
	if err != nil {
		return "", "", fmt.Errorf("generate device id: %w", err)
	}
	if err := writeIdentity(path, id, signals, computerID); err != nil {
		return "", "", err
	}
	return id, source, nil
}

// writeIdentity atomically persists the identity record.
func writeIdentity(path, id string, signals []string, computerID string) error {
	fp := ""
	if len(signals) > 0 {
		fp = signals[0]
	}
	rec := identity{DeviceID: id, Fingerprint: fp, Signals: signals, ComputerID: computerID, CreatedAt: time.Now().Unix()}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("prepare secure dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("persist identity: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("install identity: %w", err)
	}
	return nil
}

// persistDeviceID overwrites identity.json with a specific device id (keeping the
// current signals and any server-assigned computer id). Used when the server
// reconciles the agent to a canonical id during enrollment (dedup).
func persistDeviceID(secureDir, id string, signals []string) error {
	path := filepath.Join(secureDir, "identity.json")
	return writeIdentity(path, id, signals, loadComputerID(secureDir))
}

// setComputerID records the server-assigned numeric console id (from the enroll
// response) alongside the existing identity. Best-effort; no-op if there is no id.
func setComputerID(secureDir, deviceID string, signals []string, computerID string) error {
	if strings.TrimSpace(computerID) == "" {
		return nil
	}
	path := filepath.Join(secureDir, "identity.json")
	return writeIdentity(path, deviceID, signals, computerID)
}

// loadComputerID returns the persisted server-assigned computer id, or "".
func loadComputerID(secureDir string) string {
	data, err := os.ReadFile(filepath.Join(secureDir, "identity.json"))
	if err != nil {
		return ""
	}
	var cur identity
	if json.Unmarshal(data, &cur) != nil {
		return ""
	}
	return cur.ComputerID
}

// newUUID returns an RFC 4122 version-4 UUID using crypto/rand (no external dep).
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10xx
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
