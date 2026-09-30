//go:build windows

package main

import (
	"strings"

	"github.com/aegisoneuem/aegis_one_agent/internal/patch"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// handleInstallPatch installs a server-delivered LOCAL patch file via DISM and
// returns the result. The file was delivered by the server (peer-cache) and
// named in spec.FilePath; this only ever touches that local file — never the
// network. The integrity check inside InstallLocalPatch refuses the install on
// a SHA-256 mismatch.
//
// spec.HashAlgorithm is part of the wire contract for future configurability;
// the agent currently implements SHA-256 only, so a different algorithm is
// rejected fail-loud rather than silently mismatching.
func handleInstallPatch(spec *pb.InstallPatchSpec) *pb.InstallResult {
	algo := strings.ToLower(strings.TrimSpace(spec.HashAlgorithm))
	if algo != "" && algo != "sha256" {
		return &pb.InstallResult{
			Kb:      spec.Kb,
			Success: false,
			Detail:  "unsupported hash_algorithm: " + spec.HashAlgorithm + " (agent supports sha256)",
		}
	}

	res, err := patch.InstallLocalPatch(spec.FilePath, spec.ExpectedHash)
	if res == nil {
		// A nil result means the install could not even start (bad path, DISM
		// missing, hashing failure); report the error as a failure.
		detail := "install failed"
		if err != nil {
			detail = err.Error()
		}
		return &pb.InstallResult{Kb: spec.Kb, Success: false, Detail: detail}
	}

	return &pb.InstallResult{
		Kb:             spec.Kb,
		Success:        res.Success,
		RebootRequired: res.RebootRequired,
		ExitCode:       int32(res.ExitCode),
		Detail:         res.Detail,
	}
}
