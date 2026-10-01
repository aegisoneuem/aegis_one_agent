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
	"syscall"
	"time"
	"unsafe"
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
	Reason         string // concise, human failure cause (empty on success)
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
	if err := VerifyPatchFile(path, expectedSHA256); err != nil {
		return &DeployResult{Path: path, Success: false, Detail: err.Error()}, err
	}
	return RunDISM(path, installTimeout)
}

// VerifyPatchFile checks that the file exists and (when expectedSHA256 is set)
// that its SHA-256 matches, failing loud on mismatch. An empty expectedSHA256
// skips the check with a warning (dev/test only). It never touches the network.
func VerifyPatchFile(path, expectedSHA256 string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve patch path %q: %w", path, err)
	}
	if _, err := os.Stat(abs); err != nil {
		// No internet fallback: a missing file is a delivery problem, not a
		// reason to fetch anything.
		return fmt.Errorf("patch file not present at %q: %w", abs, err)
	}
	if expectedSHA256 == "" {
		slog.Warn("deploy: no expected SHA-256 provided — skipping integrity check (dev/test only)", "file", abs)
		return nil
	}
	got, err := fileSHA256(abs)
	if err != nil {
		return fmt.Errorf("hash patch file %q: %w", abs, err)
	}
	if !strings.EqualFold(got, strings.TrimSpace(expectedSHA256)) {
		slog.Error("deploy: patch integrity check FAILED — refusing to install",
			"file", abs, "expected_sha256", expectedSHA256, "actual_sha256", got)
		return fmt.Errorf("integrity check failed for %q: SHA-256 mismatch", abs)
	}
	slog.Info("deploy: patch integrity verified (SHA-256 match)", "file", abs)
	return nil
}

// RunDISM installs a LOCAL patch file (.msu/.cab) with DISM — no internet, no
// PowerShell. The caller is responsible for integrity verification beforehand
// (see VerifyPatchFile). timeout bounds the single DISM invocation.
func RunDISM(path string, timeout time.Duration) (*DeployResult, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve patch path %q: %w", path, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("patch file not present at %q: %w", abs, err)
	}

	// Disk-space pre-flight: DISM needs room to expand the package into the
	// component store (well beyond the file size). Fail fast with a clear reason
	// rather than burning minutes to discover the volume is full.
	if free, derr := freeDiskBytes(abs); derr == nil {
		if need := info.Size() * 3; free < need {
			slog.Error("deploy: insufficient disk space for install",
				"file", abs, "free_bytes", free, "need_bytes_estimate", need)
			return &DeployResult{Path: abs, Success: false,
				Reason: fmt.Sprintf("insufficient disk space: ~%d MB free, need ~%d MB", free>>20, need>>20),
				Detail: "pre-flight disk space check failed"}, nil
		}
	}

	slog.Info("deploy: installing local patch via DISM", "file", abs, "timeout", timeout.String())

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "dism.exe",
		"/online", "/add-package",
		"/packagepath:"+abs,
		"/quiet", "/norestart")

	stop := startHeartbeat("DISM install")
	out, runErr := cmd.CombinedOutput()
	stop()

	if ctx.Err() == context.DeadlineExceeded {
		return &DeployResult{Path: abs, Detail: fmt.Sprintf("DISM timed out after %s", timeout)},
			fmt.Errorf("DISM install timed out after %s", timeout)
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
		res.Reason = classifyDISMFailure(exitCode, string(out))
		// Fail loud with the concise reason + output tail so it is diagnosable.
		slog.Error("deploy: install FAILED", "file", abs, "exit_code", exitCode, "reason", res.Reason, "detail", res.Detail)
	}
	return res, nil
}

// classifyDISMFailure turns a DISM exit code and its output into a concise,
// human failure reason for troubleshooting. The full output stays in Detail
// (verbose). Matching is best-effort on known HRESULTs/phrases; unknown
// failures fall back to the exit code.
func classifyDISMFailure(exitCode int, output string) string {
	o := strings.ToLower(output)
	switch {
	case strings.Contains(o, "0x80070070") || strings.Contains(o, "not enough space") || strings.Contains(o, "disk is full"):
		return "insufficient disk space on the target volume"
	case strings.Contains(o, "0x800f081f") || strings.Contains(o, "source files could not be found"):
		return "required source files missing (component store may be corrupt — run DISM /RestoreHealth)"
	case strings.Contains(o, "0x800f0922"):
		return "install rolled back — a pending operation or a failed servicing step (reboot and retry)"
	case strings.Contains(o, "0x800f0923"):
		return "package not applicable, or a reboot is pending from a previous update"
	case strings.Contains(o, "0x80073701") || strings.Contains(o, "0x800f0831"):
		return "component store corruption (run DISM /RestoreHealth or SFC /scannow)"
	case strings.Contains(o, "0x8024") :
		return "Windows Update servicing error (" + firstHRESULT(output) + ")"
	case strings.Contains(o, "trustedinstaller") && (strings.Contains(o, "disabled") || strings.Contains(o, "cannot be started")):
		return "Windows servicing service (TrustedInstaller) is disabled or cannot start"
	case strings.Contains(o, "the system cannot find the file"):
		return "a required file was not found"
	case strings.Contains(o, "0x80070005") || strings.Contains(o, "access is denied"):
		return "access denied — the agent must run elevated (LocalSystem) to install"
	}
	if h := firstHRESULT(output); h != "" {
		return fmt.Sprintf("install failed (exit %d, %s)", exitCode, h)
	}
	return fmt.Sprintf("install failed (exit %d)", exitCode)
}

// firstHRESULT returns the first 0x######## token in s, or "".
func firstHRESULT(s string) string {
	i := strings.Index(strings.ToLower(s), "0x")
	if i < 0 {
		return ""
	}
	j := i + 2
	for j < len(s) && j < i+10 && isHexDigit(s[j]) {
		j++
	}
	if j <= i+2 {
		return ""
	}
	return s[i:j]
}

func isHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// freeDiskBytes returns the free bytes available on the volume that contains
// path, using GetDiskFreeSpaceExW.
func freeDiskBytes(path string) (int64, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	vol := filepath.VolumeName(abs) + `\`
	ptr, err := syscall.UTF16PtrFromString(vol)
	if err != nil {
		return 0, err
	}
	var freeAvail, total, totalFree uint64
	r, _, e := procGetDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(ptr)),
		uintptr(unsafe.Pointer(&freeAvail)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r == 0 {
		return 0, e
	}
	return int64(freeAvail), nil
}

var (
	modkernel32            = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpaceEx = modkernel32.NewProc("GetDiskFreeSpaceExW")
)

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
