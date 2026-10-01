//go:build windows

package main

import (
	"fmt"
	"strings"

	"github.com/aegisoneuem/aegis_one_agent/internal/patch"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// handleInstallPatch installs a server-delivered LOCAL patch file via DISM and
// returns the terminal InstallStatus (SUCCESS or FAILED). The file was delivered
// by the server (peer-cache) and named in spec.FilePath; this only ever touches
// that local file — never the network. The integrity check inside
// InstallLocalPatch refuses the install on a SHA-256 mismatch.
//
// B2b-1 reports only the terminal phase; intermediate phases (DOWNLOADING,
// VERIFYING, RUNNING) and the encrypted cache are wired in B2b-2.
//
// spec.HashAlgorithm is part of the wire contract for future configurability;
// the agent currently implements SHA-256 only, so a different algorithm is
// rejected fail-loud rather than silently mismatching.
func handleInstallPatch(spec *pb.InstallPatchSpec) *pb.InstallStatus {
	algo := strings.ToLower(strings.TrimSpace(spec.HashAlgorithm))
	if algo != "" && algo != "sha256" {
		return &pb.InstallStatus{
			Kb:     spec.Kb,
			Phase:  pb.InstallPhase_INSTALL_PHASE_FAILED,
			Reason: "unsupported hash_algorithm: " + spec.HashAlgorithm + " (agent supports sha256)",
		}
	}

	res, err := patch.InstallLocalPatch(spec.FilePath, spec.ExpectedHash)
	if res == nil {
		// A nil result means the install could not even start (bad path, DISM
		// missing, hashing failure); report the error as a failure.
		reason := "install failed"
		if err != nil {
			reason = err.Error()
		}
		return &pb.InstallStatus{Kb: spec.Kb, Phase: pb.InstallPhase_INSTALL_PHASE_FAILED, Reason: reason}
	}

	st := &pb.InstallStatus{
		Kb:             spec.Kb,
		ExitCode:       int32(res.ExitCode),
		RebootRequired: res.RebootRequired,
		Detail:         res.Detail,
	}
	if res.Success {
		st.Phase = pb.InstallPhase_INSTALL_PHASE_SUCCESS
	} else {
		st.Phase = pb.InstallPhase_INSTALL_PHASE_FAILED
		st.Reason = fmt.Sprintf("install failed (exit %d)", res.ExitCode)
	}
	return st
}
