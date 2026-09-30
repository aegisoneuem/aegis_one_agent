//go:build windows

package patch

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// installTimeout bounds a single DISM install. Cumulative updates can take many
// minutes to apply; generous but finite so a stuck install cannot hang forever.
var installTimeout = 30 * time.Minute

// DeployResult is the outcome of installing one local patch file.
type DeployResult struct {
	Path           string
	Success        bool
	RebootRequired bool
	ExitCode       int
	Detail         string // trailing DISM output (for logs/console)
}

// InstallLocalPatch installs a LOCAL patch file (.msu or .cab) with DISM — no
// internet and no PowerShell. The server delivers the file to the endpoint via
// peer-cache; this function only ever touches that local file, never the
// network. Requires Administrator (the agent runs as a LocalSystem service in
// production; a non-elevated dev run will fail).
//
// DISM was chosen over wusa.exe as Microsoft's current, recommended offline
// install path (handles .cab and .msu); revisit if Microsoft changes course.
func InstallLocalPatch(path string) (*DeployResult, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve patch path %q: %w", path, err)
	}
	if _, err := os.Stat(abs); err != nil {
		// No internet fallback: a missing file is a delivery problem, not a
		// reason to fetch anything.
		return nil, fmt.Errorf("patch file not present at %q: %w", abs, err)
	}

	slog.Info("deploy: installing local patch via DISM", "file", abs, "timeout", installTimeout.String())

	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "dism.exe",
		"/online", "/add-package",
		"/packagepath:"+abs,
		"/quiet", "/norestart")

	stop := startHeartbeat("DISM install")
	out, runErr := cmd.CombinedOutput()
	stop()

	if ctx.Err() == context.DeadlineExceeded {
		return &DeployResult{Path: abs, Detail: fmt.Sprintf("DISM timed out after %s", installTimeout)},
			fmt.Errorf("DISM install timed out after %s", installTimeout)
	}
	if cmd.ProcessState == nil {
		// DISM failed to even start (missing binary, etc.).
		return nil, fmt.Errorf("could not run DISM: %w", runErr)
	}

	// DISM exit codes: 0 = success; 3010 = success, reboot required;
	// 1641 = reboot initiated; anything else = failure.
	exitCode := cmd.ProcessState.ExitCode()
	res := &DeployResult{Path: abs, ExitCode: exitCode, Detail: tail(string(out), 600)}
	switch exitCode {
	case 0:
		res.Success = true
	case 3010, 1641:
		res.Success = true
		res.RebootRequired = true
	default:
		res.Success = false
	}

	if res.Success {
		slog.Info("deploy: install succeeded", "file", abs, "exit_code", exitCode, "reboot_required", res.RebootRequired)
	} else {
		// Fail loud with the DISM output tail so the failure is diagnosable.
		slog.Error("deploy: install FAILED", "file", abs, "exit_code", exitCode, "detail", res.Detail)
	}
	return res, nil
}

// tail returns the last n characters of s (trimmed), prefixed with "..." when
// truncated — enough DISM output to diagnose without flooding logs.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
