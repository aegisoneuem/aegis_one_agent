package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// readKeyMarker/writeKeyMarker persist a NON-SECRET "key provisioned" marker so a
// DELETED master key is distinguishable from a genuine first install. They are
// function vars so tests use an in-memory marker and never touch the real
// registry. The marker is a flag only — it reveals nothing about the key.
var (
	readKeyMarker  = platformReadKeyMarker
	writeKeyMarker = platformWriteKeyMarker
	// keyAlert is the alert sink (overridable in tests).
	keyAlert = alertKeyTampered
)

// checkKeyIntegrity detects tampering with the DPAPI-protected master key at
// startup and raises a key_tampered alert (parallel to policy_tampered), then
// self-heals so the agent stays functional:
//   - corrupt: key file present but cannot be unsealed (DPAPI unprotect fails) ->
//     alert, drop the bad blob so a fresh key is generated next.
//   - deleted: key file gone but a provisioned marker proves one existed -> alert;
//     the provider regenerates on next use. At-rest state (buffer/cache/floor)
//     resets — the TPM NV floor is the roadmap fix.
//   - first install: no key + no marker -> silent; create + mark.
//
// Honest bound: a LocalSystem/physical attacker can always delete the key on
// their own box; the goal is DETECT + ALERT + self-heal, not prevention (TPM is
// the real anchor).
func checkKeyIntegrity(secureDir string, keys secure.KeyProvider, outbox chan<- *pb.AgentMessage, deviceID string) {
	keyPath := filepath.Join(secureDir, secure.KeyFileName)
	_, statErr := os.Stat(keyPath)
	keyExists := statErr == nil
	provisioned := readKeyMarker(secureDir)

	switch {
	case keyExists:
		if _, err := keys.Key(); err != nil {
			keyAlert(outbox, deviceID, "corrupt", "master key present but cannot be unsealed: "+err.Error())
			_ = os.Remove(keyPath) // self-heal: drop the unreadable blob so the next load regenerates
			if _, rerr := keys.Key(); rerr == nil {
				_ = writeKeyMarker(secureDir)
			}
			return
		}
		if !provisioned {
			_ = writeKeyMarker(secureDir) // first good key we've recorded (or an upgrade from before this check existed)
		}
	default: // key file missing
		if provisioned {
			keyAlert(outbox, deviceID, "deleted", "master key file is missing but was provisioned before — at-rest state (buffer/cache/floor) will reset")
		}
		if _, err := keys.Key(); err == nil { // (re)create and re-mark
			_ = writeKeyMarker(secureDir)
		}
	}
}

func alertKeyTampered(outbox chan<- *pb.AgentMessage, deviceID, kind, reason string) {
	slog.Error("SECURITY: master key integrity alert", "kind", kind, "reason", reason)
	recordAuditEvent(context.Background(), outbox, auditlog.Event{
		DeviceID: deviceID, EventType: "key_tampered", Decision: kind, Reason: reason,
	})
}
