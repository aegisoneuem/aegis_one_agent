//go:build !windows

package main

import (
	"time"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// alreadyInstalled is a no-op on non-Windows platforms (installs unsupported).
func alreadyInstalled(kb string) bool { return false }

// handleInstallPatch is not supported on non-Windows platforms yet; Windows
// remediation lives in install_windows.go. Linux/macOS remediation will use
// their native package managers later.
func handleInstallPatch(spec *pb.InstallPatchSpec, installPath string, timeout time.Duration, emit phaseEmitter) *pb.InstallStatus {
	return &pb.InstallStatus{
		Kb:     spec.Kb,
		Phase:  pb.InstallPhase_INSTALL_PHASE_FAILED,
		Reason: "patch install not supported on this OS yet",
	}
}
