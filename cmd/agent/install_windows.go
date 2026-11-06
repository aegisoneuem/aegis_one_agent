//go:build windows

package main

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	"github.com/aegisoneuem/aegis_one_agent/internal/patch"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// applyTuning pushes configurable tuning values into the patch package at
// startup (Windows-only knobs).
func applyTuning(cfg config.Config) {
	patch.SetDiskMultiplier(cfg.InstallDiskMultiplier)
}

// alreadyInstalled reports whether the KB is already applied, so the worker can
// skip a redundant install. Best-effort: on error it returns false (proceed).
func alreadyInstalled(kb string) bool {
	if strings.TrimSpace(kb) == "" {
		return false
	}
	installed, err := patch.IsKBInstalled(kb)
	if err != nil {
		slog.Warn("install_patch: could not check installed state, proceeding", "kb", kb, "error", err.Error())
		return false
	}
	return installed
}

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
func handleInstallPatch(spec *pb.InstallPatchSpec, installPath string, timeout time.Duration, maxAttempts int, retryBackoff time.Duration, emit phaseEmitter) *pb.ActionStatus {
	emit(pb.ActionPhase_ACTION_PHASE_VERIFYING)
	// hash_algorithm is the operator's choice (sha256 default, or sha512);
	// VerifyPatchFile rejects an unsupported algorithm fail-loud. Verify once; the
	// bytes don't change between DISM retries.
	if err := patch.VerifyPatchFile(installPath, spec.ExpectedHash, spec.HashAlgorithm); err != nil {
		return installFailed(spec.Kb, err.Error())
	}

	if maxAttempts < 1 {
		maxAttempts = 1
	}
	emit(pb.ActionPhase_ACTION_PHASE_RUNNING)

	var res *patch.DeployResult
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		res, err = patch.RunDISM(installPath, timeout)
		if res == nil {
			// Could not even start DISM (bad path, missing binary) — not retryable.
			reason := "install failed"
			if err != nil {
				reason = err.Error()
			}
			return installFailed(spec.Kb, reason)
		}
		if res.Success || !res.Transient || attempt == maxAttempts {
			break
		}
		slog.Warn("install_patch: attempt failed (transient), retrying",
			"kb", spec.Kb, "attempt", attempt, "of", maxAttempts, "reason", res.Reason, "backoff", retryBackoff.String())
		time.Sleep(retryBackoff)
	}

	// DISM failed — some .msu (especially on Windows Server) install via wusa but
	// not DISM ("The request is not supported"). Fall back to wusa.exe (exactly what
	// the manual "install this update" does). Skip when the failure was the disk
	// pre-flight — wusa needs the same free space, so retrying there is pointless.
	if !res.Success && !strings.Contains(strings.ToLower(res.Reason), "disk space") {
		slog.Info("install_patch: DISM could not install this .msu — falling back to wusa (the manual install path)", "kb", spec.Kb, "dism_exit", res.ExitCode, "dism_reason", res.Reason)
		if wres := patch.RunWUSAInstall(installPath, timeout); wres != nil {
			switch {
			case wres.AlreadyInstalled:
				slog.Info("install_patch: wusa reports the KB is already installed — nothing to do", "kb", spec.Kb, "exit_code", wres.ExitCode)
				res = wres
			case wres.NotApplicable:
				slog.Info("install_patch: wusa reports the update is NOT APPLICABLE to this device — benign (wrong OS/build or missing prerequisite)", "kb", spec.Kb, "exit_code", wres.ExitCode)
				res = wres
			case wres.Success:
				slog.Info("install_patch: installed via wusa fallback", "kb", spec.Kb, "exit_code", wres.ExitCode, "reboot_required", wres.RebootRequired)
				res = wres
			default:
				res.Detail = strings.TrimSpace(res.Detail + "\n-- wusa fallback also failed: " + wres.Reason + " --\n" + wres.Detail)
			}
		}
	}

	st := &pb.ActionStatus{
		Kb:             spec.Kb,
		ExitCode:       int32(res.ExitCode),
		RebootRequired: res.RebootRequired,
		Detail:         res.Detail,
	}
	switch {
	case res.AlreadyInstalled:
		// The installer (DISM/wusa) found the KB already present — report it as a
		// no-op, NOT a fresh install. This catches the case where the cheap CBS
		// pre-check missed it (CBS state is not always authoritative).
		st.Phase = pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE
		st.Reason = "already installed (nothing to do)"
		st.RebootRequired = false // nothing was installed this time
	case res.NotApplicable:
		// The update does not apply to this device (wrong OS/build/arch or a
		// missing prerequisite). Benign, NOT a failure — this is the signal the
		// server's self-correcting diff uses to stop re-offering the patch.
		st.Phase = pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE
		st.Reason = res.Reason
		st.RebootRequired = false
	case res.Success:
		st.Phase = pb.ActionPhase_ACTION_PHASE_SUCCESS
		// Make the terminal status explicit about restart state so the console can
		// show "Completed" vs "Pending Restart" and the reason is clear in the logs.
		if st.RebootRequired {
			st.Reason = fmt.Sprintf("installed — RESTART REQUIRED to complete (exit %d)", res.ExitCode)
		} else {
			st.Reason = fmt.Sprintf("installed — completed, no restart needed (exit %d)", res.ExitCode)
		}
	default:
		st.Phase = pb.ActionPhase_ACTION_PHASE_FAILED
		st.Reason = res.Reason // concise, classified failure cause
		if st.Reason == "" {
			st.Reason = fmt.Sprintf("install failed (exit %d)", res.ExitCode)
		}
	}
	return st
}

func installFailed(kb, reason string) *pb.ActionStatus {
	return &pb.ActionStatus{Kb: kb, Phase: pb.ActionPhase_ACTION_PHASE_FAILED, Reason: reason}
}
