//go:build !windows

package main

import (
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// applyTuning is a no-op on non-Windows platforms (tuning knobs are Windows-only).
func applyTuning(cfg config.Config) {}

// alreadyInstalled is a no-op on non-Windows platforms (installs unsupported).
func alreadyInstalled(kb string) bool { return false }

// handleInstallPatch is not supported on non-Windows platforms yet; Windows
// remediation lives in install_windows.go. Linux/macOS remediation will use
// their native package managers later.
func handleInstallPatch(spec *pb.InstallPatchSpec, installPath string, timeout time.Duration, maxAttempts int, retryBackoff time.Duration, emit phaseEmitter) *pb.ActionStatus {
	return &pb.ActionStatus{
		Kb:     spec.Kb,
		Phase:  pb.ActionPhase_ACTION_PHASE_FAILED,
		Reason: "patch install not supported on this OS yet",
	}
}
