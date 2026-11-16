package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

const tamperWatchdogInterval = 30 * time.Second

// runTamperWatchdog self-heals the service-level protections against a local admin:
// it periodically re-applies the service DACL and, crucially, UNDOES a `sc config
// disabled` / regedit `Start=4` (which the service DACL does NOT cover — it protects
// the SCM object, not the registry key). It also re-hardens the install-dir ACL when
// the master switch is on. Only runs as the service and only while self-protected.
//
// Honest bound: a determined admin can take-ownership or taskkill faster than the
// 30s loop; this raises the bar + closes the easy persistent bypasses (disable via
// registry, DACL reset) + alerts. Absolute = TPM/PPL (Phase B).
func runTamperWatchdog(ctx context.Context, cfg config.Config, outbox chan<- *pb.AgentMessage, deviceID string) {
	defer recoverLog("tamper-watchdog")
	if !runningAsService {
		return
	}
	t := time.NewTicker(tamperWatchdogInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !(cfg.TamperProtect || passcodeStoreExists(cfg.SecureDir) || readUnprotectMarker()) {
				continue // not self-protected — nothing to guard
			}
			applyServiceDACLForState(true) // re-apply if an admin reset it
			if cfg.TamperProtect {
				hardenInstallTree() // re-tighten the install-dir ACL
			}
			if reEnableServiceStart() { // undo sc config disabled / regedit Start=4
				slog.Error("SECURITY: agent service was set to DISABLED — re-enabled by watchdog (tamper)")
				recordAuditEvent(ctx, outbox, auditlog.Event{
					DeviceID: deviceID, EventType: "service_tampered", Decision: "re-enabled",
					Reason: "service start-type was set to disabled; watchdog restored auto-start",
				})
			}
		}
	}
}
