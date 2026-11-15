package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// Overridable seams so tests use an in-memory baseline + a fake exe path + a
// capturing alert sink, and never touch the real registry or binary.
var (
	readBinaryBaseline  = platformReadBinaryBaseline
	writeBinaryBaseline = platformWriteBinaryBaseline
	binaryAlert         = alertBinaryTampered
	osExecutable        = os.Executable
)

// checkBinaryIntegrity detects unexpected modification of the agent's own
// executable at startup, keeping a {version, hash} baseline in the registry:
//   - same version, DIFFERENT hash -> the binary was swapped/patched in place
//     without a version change -> binary_tampered alert. The baseline is then
//     re-armed to the new hash so the alert fires on the CHANGE event, not on
//     every boot.
//   - different version -> a legitimate update -> refresh the baseline silently.
//   - no baseline (first run) -> record silently.
//
// Opt-in (AEGIS_BINARY_INTEGRITY) so routine same-version dev rebuilds don't
// alert. Honest bound: a tampered binary could skip its own check and a
// LocalSystem attacker can rewrite the baseline — this is DETECT + ALERT +
// defense-in-depth (catches corruption + naive replacement), not prevention.
// Authenticode self-verification + TPM are the stronger anchors (roadmap).
func checkBinaryIntegrity(outbox chan<- *pb.AgentMessage, deviceID, version string) {
	exe, err := osExecutable()
	if err != nil {
		return
	}
	hash, err := fileSHA256(exe)
	if err != nil {
		return
	}
	prevVer, prevHash := readBinaryBaseline()
	switch {
	case prevHash == "": // first run — establish the baseline
		_ = writeBinaryBaseline(version, hash)
		slog.Info("binary integrity: baseline recorded", "version", version)
	case prevVer == version && prevHash != hash:
		binaryAlert(outbox, deviceID, "modified",
			"agent binary changed without a version change (version "+version+", hash differs from baseline) — possible tampering")
		_ = writeBinaryBaseline(version, hash) // re-arm the tripwire
	case prevVer != version:
		_ = writeBinaryBaseline(version, hash) // legitimate update
		slog.Info("binary integrity: baseline refreshed for new version", "old_version", prevVer, "new_version", version)
	}
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func alertBinaryTampered(outbox chan<- *pb.AgentMessage, deviceID, kind, reason string) {
	slog.Error("SECURITY: binary integrity alert", "kind", kind, "reason", reason)
	recordAuditEvent(context.Background(), outbox, auditlog.Event{
		DeviceID: deviceID, EventType: "binary_tampered", Decision: kind, Reason: reason,
	})
}
