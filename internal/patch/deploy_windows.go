//go:build windows

package patch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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
//
// expectedSHA256 (hex) is the file integrity check: when non-empty the file's
// SHA-256 must match or the install is refused before DISM runs. In production
// the server always supplies it (inside the Ed25519-signed install command);
// an empty value skips the check and is for dev/test only.
func InstallLocalPatch(path, expectedSHA256 string) (*DeployResult, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve patch path %q: %w", path, err)
	}
	if _, err := os.Stat(abs); err != nil {
		// No internet fallback: a missing file is a delivery problem, not a
		// reason to fetch anything.
		return nil, fmt.Errorf("patch file not present at %q: %w", abs, err)
	}

	// Integrity: verify the delivered bytes before trusting them to DISM.
	if expectedSHA256 != "" {
		got, err := fileSHA256(abs)
		if err != nil {
			return nil, fmt.Errorf("hash patch file %q: %w", abs, err)
		}
		if !strings.EqualFold(got, strings.TrimSpace(expectedSHA256)) {
			slog.Error("deploy: patch integrity check FAILED — refusing to install",
				"file", abs, "expected_sha256", expectedSHA256, "actual_sha256", got)
			return &DeployResult{Path: abs, Success: false, Detail: "integrity check failed: SHA-256 mismatch"},
				fmt.Errorf("integrity check failed for %q: SHA-256 mismatch", abs)
		}
		slog.Info("deploy: patch integrity verified (SHA-256 match)", "file", abs)
	} else {
		slog.Warn("deploy: no expected SHA-256 provided — skipping integrity check (dev/test only)", "file", abs)
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

// fileSHA256 returns the hex-encoded SHA-256 of the file at path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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
