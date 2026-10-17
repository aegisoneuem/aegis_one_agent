//go:build windows

package patch

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// Windows Update success/failure codes wusa can exit with on an uninstall.
const (
	wuSRebootRequired      = 0x240005   // WU_S_REBOOT_REQUIRED — done, reboot finishes it
	wuENotApplicable       = 0x80240017 // WU_E_NOT_APPLICABLE — not installed / nothing to do
	wuEUninstallNotAllowed = 0x80240028 // WU_E_UNINSTALL_NOT_ALLOWED
)

// UninstallKBviaWusa removes an update with `wusa.exe /uninstall /kb:N /quiet
// /norestart` — the SAME engine Control Panel's "Installed Updates" drives. It is
// the one engine that removes a CUMULATIVE update (LCU): DISM refuses those with
// 0x800f0825 (by design) and WUA does not track them by KB. Verified on Server
// 2022: quiet wusa staged the LCU removal (entry left the installed list; the
// reboot completes it).
//
// Caveat the caller handles: some session-0 service contexts reject quiet wusa
// uninstall with error 87 — then the chain reports the honest combined failure.
func UninstallKBviaWusa(kb string, timeout time.Duration) *DeployResult {
	kb = normalizeKB(kb)
	if kb == "" {
		return &DeployResult{Reason: "no KB specified for uninstall"}
	}
	if timeout <= 0 {
		timeout = installTimeout
	}
	slog.Info("deploy: uninstalling via wusa (Control Panel's engine)", "kb", "KB"+kb, "timeout", timeout.String())

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "wusa.exe", "/uninstall", "/kb:"+kb, "/quiet", "/norestart")
	slog.Debug("deploy: running wusa", "command", strings.Join(cmd.Args, " "))
	stop := startHeartbeat("wusa uninstall")
	out, runErr := cmd.CombinedOutput()
	stop()

	if ctx.Err() == context.DeadlineExceeded {
		return &DeployResult{Reason: fmt.Sprintf("wusa uninstall timed out after %s", timeout)}
	}
	if cmd.ProcessState == nil {
		return &DeployResult{Reason: "could not run wusa", Detail: fmt.Sprintf("%v", runErr)}
	}

	exit := cmd.ProcessState.ExitCode()
	res := &DeployResult{ExitCode: exit, Detail: tail(string(out), 600)}
	switch uint32(exit) {
	case 0:
		res.Success = true
	case 3010, 1641, wuSRebootRequired:
		res.Success, res.RebootRequired = true, true
	case wuENotApplicable:
		res.NotApplicable = true
		res.Reason = "wusa: update not installed (nothing to uninstall)"
	case wuEUninstallNotAllowed:
		res.Reason = "Windows does not allow uninstalling this update (wusa 0x80240028)"
	case 87:
		res.Reason = "wusa rejected the silent uninstall in this session (error 87 — known service-context limitation)"
	default:
		res.Reason = fmt.Sprintf("wusa uninstall failed (exit %d / 0x%X)", exit, uint32(exit))
	}
	if res.Success {
		slog.Info("deploy: wusa uninstall succeeded", "kb", "KB"+kb, "exit_code", exit, "reboot_required", res.RebootRequired)
	} else {
		slog.Warn("deploy: wusa uninstall attempt failed", "kb", "KB"+kb, "exit_code", exit, "reason", res.Reason)
	}
	return res
}
