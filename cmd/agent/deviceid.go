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
// the stable device id to the hardware fingerprint it was created under, so a
// clone/reimage (fingerprint changed) can be detected and a fresh id issued.
type identity struct {
	DeviceID    string `json:"device_id"`
	Fingerprint string `json:"fingerprint"` // hardware fingerprint at creation time
	CreatedAt   int64  `json:"created_at"`
}

// resolveDeviceID returns the agent's stable unique device id and where it came
// from. Precedence:
//  1. explicit override (AEGIS_DEVICE_ID) — testing or a server-pinned id;
//  2. persisted identity whose fingerprint still MATCHES this machine → reuse;
//  3. persisted identity whose fingerprint CHANGED → clone/reimage → new id;
//  4. no identity yet → generate a new id.
//
// fingerprint is the current hardware fingerprint (may be "" if it could not be
// read — in that case a persisted id is reused as-is, no clone detection).
func resolveDeviceID(override, secureDir, fingerprint string) (id, source string, err error) {
	if v := strings.TrimSpace(override); v != "" {
		return v, "config override", nil
	}
	path := filepath.Join(secureDir, "identity.json")

	if data, rerr := os.ReadFile(path); rerr == nil {
		var cur identity
		if json.Unmarshal(data, &cur) == nil && strings.TrimSpace(cur.DeviceID) != "" {
			// Clone/reimage detection: a changed fingerprint means this id was
			// created on a DIFFERENT machine (e.g. a VM cloned from a template that
			// had the agent baked in) — issue a fresh id so we don't collide.
			if fingerprint != "" && cur.Fingerprint != "" && cur.Fingerprint != fingerprint {
				return writeNewIdentity(path, fingerprint, "regenerated (hardware changed — clone/reimage)")
			}
			return cur.DeviceID, "persisted", nil
		}
	}
	return writeNewIdentity(path, fingerprint, "generated")
}

func writeNewIdentity(path, fingerprint, source string) (string, string, error) {
	id, err := newUUID()
	if err != nil {
		return "", "", fmt.Errorf("generate device id: %w", err)
	}
	rec := identity{DeviceID: id, Fingerprint: fingerprint, CreatedAt: time.Now().Unix()}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", "", fmt.Errorf("prepare secure dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", "", fmt.Errorf("persist identity: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", "", fmt.Errorf("install identity: %w", err)
	}
	return id, source, nil
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
