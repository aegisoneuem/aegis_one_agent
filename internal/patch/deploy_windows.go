//go:build windows

package patch

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/aegisoneuem/aegis_one_agent/internal/logging"
)

// installTimeout bounds a single DISM install. Cumulative updates can take many
// minutes to apply; generous but finite so a stuck install cannot hang forever.
var installTimeout = 30 * time.Minute

// diskSpaceMultiplier is how many times the patch file size must be free on the
// target volume before install (DISM extracts the package + grows the component
// store). Set from config via SetDiskMultiplier. Default 2 (file + ~1x working
// space) — 3x was too aggressive for multi-GB cumulative updates (e.g. a 4.3GB
// CU would demand ~13GB free). Admins raise it for extra safety or lower it on
// tight volumes.
var diskSpaceMultiplier int64 = 2

// SetDiskMultiplier overrides the disk-space pre-flight multiplier (config).
func SetDiskMultiplier(n int64) {
	if n > 0 {
		diskSpaceMultiplier = n
	}
}

// DeployResult is the outcome of installing one local patch file.

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
	if err := VerifyPatchFile(path, expectedSHA256, "sha256"); err != nil {
		return &DeployResult{Path: path, Success: false, Detail: err.Error()}, err
	}
	return RunDISM(path, installTimeout)
}

// VerifyPatchFile checks that the file exists and (when expectedHash is set) that
// its digest matches, failing loud on mismatch. algo selects the digest
// ("sha256" default, or "sha512"); the patching operator chooses per their
// policy. An empty expectedHash skips the check with a warning (dev/test only).
// It never touches the network.
func VerifyPatchFile(path, expectedHash, algo string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve patch path %q: %w", path, err)
	}
	if _, err := os.Stat(abs); err != nil {
		// No internet fallback: a missing file is a delivery problem, not a
		// reason to fetch anything.
		return fmt.Errorf("patch file not present at %q: %w", abs, err)
	}
	normAlgo := normalizeHashAlgo(algo)
	if expectedHash == "" {
		slog.Warn("deploy: no expected hash provided — skipping integrity check (dev/test only)", "file", abs)
		return nil
	}
	got, err := fileHash(abs, normAlgo)
	if err != nil {
		return fmt.Errorf("hash patch file %q: %w", abs, err)
	}
	logging.Trace("deploy: computed file hash", "file", abs, "algo", normAlgo, "hash", got)
	if !strings.EqualFold(got, strings.TrimSpace(expectedHash)) {
		slog.Error("deploy: patch integrity check FAILED — refusing to install",
			"file", abs, "algo", normAlgo, "expected", expectedHash, "actual", got)
		return fmt.Errorf("integrity check failed for %q: %s mismatch", abs, normAlgo)
	}
	slog.Info("deploy: patch integrity verified", "file", abs, "algo", normAlgo)
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
		if need := info.Size() * diskSpaceMultiplier; free < need {
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

	logging.Trace("deploy: running DISM", "command", strings.Join(cmd.Args, " "))
	stop := startHeartbeat("DISM install")
	out, runErr := cmd.CombinedOutput()
	stop()
	// Verbose: the COMPLETE DISM output (not just the tail) for deep troubleshooting.
	logging.Trace("deploy: DISM full output", "file", abs, "output", strings.TrimSpace(string(out)))

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
		slog.Info("deploy: DISM install succeeded", "file", abs, "exit_code", exitCode, "reboot_required", res.RebootRequired)
	} else {
		res.Reason, res.Transient = classifyDISMFailure(exitCode, string(out))
		// This is the DISM ATTEMPT result, not the final verdict (the caller may fall
		// back to wusa and will log the final SUCCESS/FAILED). Log at WARN so a DISM
		// miss that the fallback recovers doesn't look like a hard failure.
		slog.Warn("deploy: DISM attempt failed", "file", abs, "exit_code", exitCode, "reason", res.Reason, "transient", res.Transient, "detail", res.Detail)
	}
	return res, nil
}

// RunWUSAInstall installs a LOCAL .msu via wusa.exe (the Windows Update Standalone
// Installer — exactly what double-clicking a .msu does). It is the FALLBACK for
// when DISM cannot install a given .msu ("The request is not supported"), which
// happens for some packages, especially on Windows Server. No internet; the caller
// verifies integrity first. timeout bounds the single wusa invocation.
func RunWUSAInstall(path string, timeout time.Duration) *DeployResult {
	abs, err := filepath.Abs(path)
	if err != nil {
		return &DeployResult{Reason: fmt.Sprintf("resolve patch path %q: %v", path, err)}
	}
	slog.Info("deploy: installing local patch via wusa (fallback)", "file", abs, "timeout", timeout.String())

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "wusa.exe", abs, "/quiet", "/norestart")
	stop := startHeartbeat("wusa install")
	out, runErr := cmd.CombinedOutput()
	stop()

	if ctx.Err() == context.DeadlineExceeded {
		return &DeployResult{Path: abs, Reason: fmt.Sprintf("wusa timed out after %s", timeout), Detail: "wusa timed out"}
	}
	if cmd.ProcessState == nil {
		return &DeployResult{Path: abs, Reason: "could not run wusa", Detail: fmt.Sprintf("%v", runErr)}
	}

	exitCode := cmd.ProcessState.ExitCode()
	res := &DeployResult{Path: abs, ExitCode: exitCode, Detail: tail(string(out), 600)}
	switch exitCode {
	case 0:
		res.Success = true
	case 3010, 1641:
		res.Success = true
		res.RebootRequired = true
	case 0x240006: // WU_S_ALREADY_INSTALLED — already present; treat as success/no-op
		res.Success = true
	default:
		res.Success = false
		res.Reason = fmt.Sprintf("wusa install failed (exit %d / 0x%X)", exitCode, uint32(exitCode))
	}
	if res.Success {
		slog.Info("deploy: wusa install succeeded", "file", abs, "exit_code", exitCode, "reboot_required", res.RebootRequired)
	}
	return res
}

// classifyDISMFailure turns a DISM exit code and its output into a concise,
// human failure reason for troubleshooting. The full output stays in Detail
// (verbose). Matching is best-effort on known HRESULTs/phrases; unknown
// failures fall back to the exit code.
// classifyDISMFailure returns a concise human reason and whether the failure
// looks transient (safe to retry). Permanent causes (disk space, corruption,
// not-applicable, access-denied, reboot-pending) are NOT retried; busy/sharing/
// WU-service hiccups and unknown failures are.
func classifyDISMFailure(exitCode int, output string) (reason string, transient bool) {
	o := strings.ToLower(output)
	switch {
	case strings.Contains(o, "0x80070070") || strings.Contains(o, "not enough space") || strings.Contains(o, "disk is full"):
		return "insufficient disk space on the target volume", false
	case strings.Contains(o, "0x800f081f") || strings.Contains(o, "source files could not be found"):
		return "required source files missing (component store may be corrupt — run DISM /RestoreHealth)", false
	case strings.Contains(o, "0x800f0923"):
		return "package not applicable, or a reboot is pending from a previous update", false
	case exitCode == 50 || strings.Contains(o, "0x80070032") || strings.Contains(o, "the request is not supported"):
		// DISM cannot install THIS .msu (it is a Windows-Update-style package). The
		// patch is usually fine — the install path falls back to wusa.exe. Not a
		// "wrong package / not applicable" error.
		return "DISM does not support installing this .msu (exit 50, 'not supported') — handled by the wusa fallback", false
	case strings.Contains(o, "0x800f081e"):
		return "patch not applicable to this system (already superseded, or not for this OS/build)", false
	case strings.Contains(o, "0x80073701") || strings.Contains(o, "0x800f0831"):
		return "component store corruption (run DISM /RestoreHealth or SFC /scannow)", false
	case strings.Contains(o, "trustedinstaller") && (strings.Contains(o, "disabled") || strings.Contains(o, "cannot be started")):
		return "Windows servicing service (TrustedInstaller) is disabled or cannot start", false
	case strings.Contains(o, "the system cannot find the file"):
		return "a required file was not found", false
	case strings.Contains(o, "0x80070005") || strings.Contains(o, "access is denied"):
		return "access denied — the agent must run elevated (LocalSystem) to install", false
	case strings.Contains(o, "0x800f0922"):
		return "install rolled back — a pending operation or a failed servicing step (retry may help)", true
	case strings.Contains(o, "0x80070020") || strings.Contains(o, "being used by another process") || strings.Contains(o, "another installation is") || strings.Contains(o, "in progress"):
		return "servicing stack busy / another operation in progress (retry)", true
	case strings.Contains(o, "0x8024"):
		return "Windows Update servicing error (" + firstHRESULT(output) + ")", true
	}
	// Unknown failure: treat as PERMANENT (do not retry). The genuinely-transient
	// causes (rolled-back, servicing-stack busy, WU 0x8024) are enumerated above;
	// anything else is far more likely a non-retryable condition (not-applicable,
	// unsupported, bad package) and blind retries just delay a clear failure.
	if h := firstHRESULT(output); h != "" {
		return fmt.Sprintf("install failed (exit %d, %s)", exitCode, h), false
	}
	return fmt.Sprintf("install failed (exit %d)", exitCode), false
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

// normalizeHashAlgo canonicalizes an algorithm name ("SHA-256" → "sha256").
// Empty defaults to sha256.
func normalizeHashAlgo(algo string) string {
	a := strings.ToLower(strings.TrimSpace(algo))
	a = strings.ReplaceAll(a, "-", "")
	if a == "" {
		return "sha256"
	}
	return a
}

// fileHash returns the hex-encoded digest of the file at path using the given
// algorithm ("sha256" or "sha512"). The operator chooses per their policy.
func fileHash(path, algo string) (string, error) {
	var h hash.Hash
	switch normalizeHashAlgo(algo) {
	case "sha256":
		h = sha256.New()
	case "sha512":
		h = sha512.New()
	default:
		return "", fmt.Errorf("unsupported hash algorithm %q (supported: sha256, sha512)", algo)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
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
