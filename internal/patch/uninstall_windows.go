//go:build windows

package patch

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"time"
)

// UninstallKB removes an installed update by KB using wusa.exe (the native Windows
// Update Standalone Installer) — no PowerShell, no internet. wusa uninstalls by KB
// number directly; DISM would require resolving the full CBS package name first.
// The caller should confirm the KB is actually installed first (idempotency). The
// timeout bounds the call.
func UninstallKB(kb string, timeout time.Duration) *DeployResult {
	kb = normalizeKB(kb) // strip optional "KB" prefix -> digits
	if kb == "" {
		return &DeployResult{Success: false, Reason: "no KB specified for uninstall"}
	}

	slog.Info("deploy: uninstalling update via wusa", "kb", "KB"+kb, "timeout", timeout.String())
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "wusa.exe", "/uninstall", "/kb:"+kb, "/quiet", "/norestart")
	stop := startHeartbeat("wusa uninstall")
	out, runErr := cmd.CombinedOutput()
	stop()

	if ctx.Err() == context.DeadlineExceeded {
		return &DeployResult{Reason: fmt.Sprintf("uninstall timed out after %s", timeout),
			Detail: fmt.Sprintf("wusa timed out after %s", timeout)}
	}
	if cmd.ProcessState == nil {
		return &DeployResult{Reason: "could not run wusa", Detail: fmt.Sprintf("%v", runErr)}
	}

	exitCode := cmd.ProcessState.ExitCode()
	res := &DeployResult{ExitCode: exitCode, Detail: tail(string(out), 600)}
	// wusa exit codes mirror Windows Update: 0 = success; 3010/1641 = reboot needed.
	switch exitCode {
	case 0:
		res.Success = true
	case 3010, 1641:
		res.Success = true
		res.RebootRequired = true
	default:
		res.Success = false
		res.Reason = fmt.Sprintf("uninstall failed (exit %d / 0x%X)", exitCode, uint32(exitCode))
	}

	if res.Success {
		slog.Info("deploy: uninstall succeeded", "kb", "KB"+kb, "exit_code", exitCode, "reboot_required", res.RebootRequired)
	} else {
		slog.Error("deploy: uninstall FAILED", "kb", "KB"+kb, "exit_code", exitCode, "reason", res.Reason)
	}
	return res
}
