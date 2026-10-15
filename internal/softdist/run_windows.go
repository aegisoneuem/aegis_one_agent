//go:build windows

package softdist

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
	"time"
)

// defaultTimeout bounds a single installer run when the spec sets none. Software
// installers are usually quick, but a large MSI or a bundled .exe can take
// minutes — generous but finite so a stuck installer cannot hang forever.
const defaultTimeout = 30 * time.Minute

// Run installs (or uninstalls) one local installer with NO internet. The caller
// (the agent's softdist worker) has already serialized this against patches and
// other deploys via the shared install lane, so only one installer runs at once.
func Run(ctx context.Context, p Params) Result {
	abs, err := filepath.Abs(p.FilePath)
	if err != nil {
		return Result{Reason: fmt.Sprintf("resolve installer path %q: %v", p.FilePath, err)}
	}
	if _, err := os.Stat(abs); err != nil {
		// No internet fallback: a missing installer is a delivery problem, not a
		// reason to fetch anything.
		return Result{Reason: fmt.Sprintf("installer not present at %q (delivery problem)", abs), Detail: err.Error()}
	}

	// Integrity: when the server supplies a hash, the file must match before we run
	// anything — fail loud on mismatch. An empty hash skips the check (dev/test only).
	if strings.TrimSpace(p.ExpectedHash) != "" {
		got, herr := fileHash(abs, p.HashAlgorithm)
		if herr != nil {
			return Result{Reason: "could not hash installer for integrity check", Detail: herr.Error()}
		}
		if !strings.EqualFold(got, strings.TrimSpace(p.ExpectedHash)) {
			slog.Error("softdist: installer integrity check FAILED — refusing to run",
				"file", abs, "expected", p.ExpectedHash, "actual", got)
			return Result{Reason: "integrity check failed (hash mismatch) — refusing to run"}
		}
		slog.Info("softdist: installer integrity verified", "file", abs)
	}

	bin, args, err := buildInvocation(p, abs)
	if err != nil {
		return Result{Reason: err.Error()}
	}

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(rctx, bin, args...)
	slog.Info("softdist: running installer", "name", p.Name, "bin", bin, "file", abs, "timeout", timeout.String())
	slog.Debug("softdist: command", "command", strings.Join(cmd.Args, " "))
	out, runErr := cmd.CombinedOutput()

	if rctx.Err() == context.DeadlineExceeded {
		return Result{Reason: fmt.Sprintf("installer timed out after %s", timeout), Detail: tail(string(out), 600)}
	}
	if cmd.ProcessState == nil {
		// The installer failed to even start (missing binary, bad path, etc.).
		return Result{Reason: "could not start installer", Detail: fmt.Sprintf("%v", runErr)}
	}
	return classify(p, cmd.ProcessState.ExitCode(), string(out))
}

// classify maps an installer exit code to the terminal result. 0 succeeds;
// 3010/1641 succeed but need a reboot (same convention as Windows patches); the
// spec may name extra success codes. A few well-known MSI failures get a clear
// reason: 1618 = another install is already running (Windows serializes MSI),
// 1605 on an uninstall = the product is not installed (nothing to do).
func classify(p Params, exit int, out string) Result {
	res := Result{ExitCode: exit, Detail: tail(out, 600)}
	switch exit {
	case 0:
		res.Success = true
	case 3010, 1641:
		res.Success, res.RebootRequired = true, true
	default:
		for _, c := range p.SuccessCodes {
			if int(c) == exit {
				res.Success = true
				break
			}
		}
	}
	if res.Success {
		if res.RebootRequired {
			res.Reason = "installed — RESTART REQUIRED to complete"
		} else {
			res.Reason = "installed — completed, no restart needed"
		}
		return res
	}
	switch {
	case exit == 1605 && strings.EqualFold(strings.TrimSpace(p.Action), "uninstall"):
		res.NotApplicable = true
		res.Reason = "product not installed (nothing to uninstall)"
	case exit == 1618:
		res.Reason = "another installer is already running on this machine (Windows allows one MSI at a time, exit 1618)"
	case exit == 1619:
		res.Reason = "installer package could not be opened (exit 1619 — bad path or corrupt package)"
	case exit == 1603:
		res.Reason = "fatal error during installation (exit 1603)"
	default:
		res.Reason = fmt.Sprintf("installer failed (exit %d)", exit)
	}
	return res
}

// buildInvocation chooses the process + args for the installer type. "auto" (or
// empty) infers from the file extension. Silent/unattended flags come from the
// payload (vendor-specific); only msi gets a sane default when none are given.
func buildInvocation(p Params, abs string) (bin string, args []string, err error) {
	typ := strings.ToLower(strings.TrimSpace(p.InstallerType))
	if typ == "" || typ == "auto" {
		switch strings.ToLower(filepath.Ext(abs)) {
		case ".msi":
			typ = "msi"
		case ".ps1":
			typ = "ps1"
		case ".bat", ".cmd":
			typ = "bat"
		default:
			typ = "exe"
		}
	}
	// Simple whitespace split: silent args are flags, not quoted paths. The
	// installer PATH is passed as its own arg (not via a shell), so spaces in the
	// path are safe regardless.
	extra := strings.Fields(p.SilentArgs)
	uninstall := strings.EqualFold(strings.TrimSpace(p.Action), "uninstall")

	switch typ {
	case "msi":
		flag := "/i"
		if uninstall {
			flag = "/x"
		}
		if len(extra) == 0 {
			extra = []string{"/qn", "/norestart"} // default MSI silent + no auto-reboot
		}
		return "msiexec.exe", append([]string{flag, abs}, extra...), nil
	case "ps1":
		// Executing a .ps1 PAYLOAD the server shipped — a deploy action, not the
		// agent using PowerShell for its own logic (which it never does).
		return "powershell.exe", append([]string{"-NonInteractive", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", abs}, extra...), nil
	case "bat":
		return "cmd.exe", append([]string{"/c", abs}, extra...), nil
	case "exe":
		return abs, extra, nil
	default:
		return "", nil, fmt.Errorf("unknown installer_type %q (want msi|exe|ps1|bat|auto)", p.InstallerType)
	}
}

// fileHash returns the hex-encoded digest of the file using the given algorithm
// ("sha256" default, or "sha512").
func fileHash(path, algo string) (string, error) {
	var h hash.Hash
	switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(algo)), "-", "") {
	case "", "sha256":
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
// truncated — enough installer output to diagnose without flooding logs.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
