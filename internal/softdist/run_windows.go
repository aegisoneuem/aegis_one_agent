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
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// defaultTimeout bounds a single installer run when the spec sets none. Software
// installers are usually quick, but a large MSI or a bundled .exe can take
// minutes — generous but finite so a stuck installer cannot hang forever.
const defaultTimeout = 30 * time.Minute

// Run installs (or uninstalls) one local installer with NO internet. The caller
// (the agent's softdist worker) has already serialized this against patches and
// other deploys via the shared install lane, so only one installer runs at once.
func Run(ctx context.Context, p Params) Result {
	// Fileless MSI uninstall: when we know the MSI ProductCode and no installer file
	// is present, Windows Installer removes the product from its own database —
	// msiexec /x {GUID}. No file means no integrity/signature check to run (there is
	// nothing to verify); this is the standard-UEM way to uninstall an MSI product.
	if isFilelessMSIUninstall(p) {
		pc := strings.TrimSpace(p.ProductCode)
		logPath := filepath.Join(os.TempDir(), "aegis-uninstall-"+sanitizeCode(pc)+".log")
		bin, args := msiUninstallByCode(p, pc, logPath)
		return execInstaller(ctx, p, bin, args, logPath)
	}

	// Fileless registry uninstall: a non-MSI product is removed by running the
	// command the vendor registered in the Windows Uninstall hive — no installer file.
	if isFilelessRegistryUninstall(p) {
		return runRegistryUninstall(ctx, p)
	}

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

	// Publisher check (standard-UEM): when required, the installer must carry a
	// valid Authenticode signature trusted by this machine, or we refuse to run it.
	if p.RequireSignature {
		if err := verifyAuthenticode(abs); err != nil {
			slog.Error("softdist: publisher signature check FAILED — refusing to run", "file", abs, "error", err.Error())
			return Result{Reason: "publisher signature check failed (" + err.Error() + ") — refusing to run"}
		}
		slog.Info("softdist: publisher signature verified (Authenticode)", "file", abs)
	}

	// MSI verbose log: msiexec /qn prints nothing to the console, so without this
	// a failure (e.g. 1603) has no detail to diagnose. Standard UEM always captures
	// the MSI log; we write it next to the installer, fold its tail into Detail, and
	// remove it. buildInvocation adds /l*v only for msi; the file is absent otherwise.
	logPath := abs + ".aegis-install.log"
	bin, args, err := buildInvocation(p, abs, logPath)
	if err != nil {
		return Result{Reason: err.Error()}
	}
	return execInstaller(ctx, p, bin, args, logPath)
}

// isFilelessMSIUninstall reports whether this is an uninstall we can run by MSI
// ProductCode alone (no installer file): an MSI-typed deploy with a ProductCode and
// no local file path. installer_type "" / "auto" is treated as MSI here because a
// ProductCode is inherently an MSI concept.
func isFilelessMSIUninstall(p Params) bool {
	if !strings.EqualFold(strings.TrimSpace(p.Action), "uninstall") {
		return false
	}
	if strings.TrimSpace(p.ProductCode) == "" || strings.TrimSpace(p.FilePath) != "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(p.InstallerType)) {
	case "", "auto", "msi":
		return true
	default:
		return false
	}
}

// msiUninstallByCode builds msiexec /x {ProductCode} with the payload's silent args
// (default /qn /norestart) and a verbose log for diagnosis.
func msiUninstallByCode(p Params, productCode, logPath string) (bin string, args []string) {
	extra := strings.Fields(p.SilentArgs)
	if len(extra) == 0 {
		extra = []string{"/qn", "/norestart"}
	}
	args = append([]string{"/x", productCode}, extra...)
	if logPath != "" {
		args = append(args, "/l*v", logPath)
	}
	return "msiexec.exe", args
}

// isFilelessRegistryUninstall reports whether this is a non-MSI uninstall we can run
// from the registry Uninstall hive (no installer file): an uninstall with an
// uninstall_key or display_name and no local file. MSI-by-ProductCode takes
// precedence (checked first in Run).
func isFilelessRegistryUninstall(p Params) bool {
	if !strings.EqualFold(strings.TrimSpace(p.Action), "uninstall") {
		return false
	}
	if strings.TrimSpace(p.FilePath) != "" || isFilelessMSIUninstall(p) {
		return false
	}
	return strings.TrimSpace(p.UninstallKey) != "" || strings.TrimSpace(p.DisplayName) != ""
}

// runRegistryUninstall removes a non-MSI product using the command the vendor
// registered in the Windows Uninstall hive. QuietUninstallString (vendor-silent) is
// preferred; otherwise UninstallString runs with the admin-supplied SilentArgs
// appended. The agent is a faithful executor — it runs the registered command and the
// admin's args, it does not invent silent switches. An absent product is a no-op
// (NOT_APPLICABLE), matching the MSI idempotency behavior.
func runRegistryUninstall(ctx context.Context, p Params) Result {
	app := FindInstalledApp(p.UninstallKey, p.DisplayName)
	if app == nil {
		return Result{NotApplicable: true, Reason: "product not installed (nothing to uninstall)"}
	}

	cmdline := strings.TrimSpace(app.QuietUninstallString)
	vendorSilent := cmdline != ""
	if !vendorSilent {
		cmdline = strings.TrimSpace(app.UninstallString)
	}
	if cmdline == "" {
		return Result{Reason: fmt.Sprintf("no uninstall command registered for %q", app.DisplayName)}
	}

	argv, err := commandLineToArgv(cmdline)
	if err != nil || len(argv) == 0 {
		return Result{Reason: fmt.Sprintf("could not parse uninstall command %q", cmdline), Detail: fmt.Sprintf("%v", err)}
	}
	bin, args := argv[0], argv[1:]
	// Only append admin silent args when the vendor did not give a silent command —
	// QuietUninstallString is already unattended.
	if !vendorSilent {
		args = append(args, strings.Fields(p.SilentArgs)...)
	}
	slog.Info("softdist: uninstalling by registry entry", "name", app.DisplayName, "key", app.Key, "vendor_silent", vendorSilent)
	return execInstaller(ctx, p, bin, args, "")
}

// commandLineToArgv splits a Windows command-line string into argv using the OS's own
// CommandLineToArgvW (shell32) — the same parser CreateProcess/cmd use — so quoted
// paths and embedded spaces are handled correctly. The command is then executed
// directly (never via a shell), so there is no shell-injection surface.
func commandLineToArgv(cmdline string) ([]string, error) {
	p, err := windows.UTF16PtrFromString(cmdline)
	if err != nil {
		return nil, err
	}
	var argc int32
	argv, err := windows.CommandLineToArgv(p, &argc)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(argv))) //nolint:errcheck
	out := make([]string, 0, argc)
	for i := int32(0); i < argc; i++ {
		out = append(out, windows.UTF16PtrToString(&argv[i][0]))
	}
	return out, nil
}

// sanitizeCode strips a ProductCode GUID down to a filename-safe token for the log.
func sanitizeCode(code string) string {
	repl := strings.NewReplacer("{", "", "}", "", " ", "")
	return repl.Replace(code)
}

// execInstaller runs the chosen installer process, bounds it with a timeout, folds
// the MSI log tail (when logPath is set) into Detail, and maps the exit code.
func execInstaller(ctx context.Context, p Params, bin string, args []string, logPath string) Result {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(rctx, bin, args...)
	slog.Info("softdist: running installer", "name", p.Name, "bin", bin, "args", strings.Join(args, " "), "timeout", timeout.String())
	out, runErr := cmd.CombinedOutput()

	// Prefer the MSI log tail over the (empty) console output for Detail.
	detail := tail(string(out), 600)
	if logPath != "" {
		if logTail := readInstallLog(logPath); logTail != "" {
			detail = logTail
		}
		_ = os.Remove(logPath)
	}

	if rctx.Err() == context.DeadlineExceeded {
		return Result{Reason: fmt.Sprintf("installer timed out after %s", timeout), Detail: detail}
	}
	if cmd.ProcessState == nil {
		// The installer failed to even start (missing binary, bad path, etc.).
		return Result{Reason: "could not start installer", Detail: fmt.Sprintf("%v", runErr)}
	}
	res := classify(p, cmd.ProcessState.ExitCode(), string(out))
	res.Detail = detail
	return res
}

// readInstallLog returns the tail of an MSI log (where the failing action and
// return codes appear). MSI logs are commonly UTF-16LE; decode before tailing.
func readInstallLog(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return tail(decodeLog(data), 2000)
}

// decodeLog turns an MSI log file's bytes into text, handling the UTF-16LE that
// msiexec usually writes (with or without a BOM).
func decodeLog(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		b = b[2:]
	}
	nul := 0
	for i := 0; i < len(b) && i < 400; i++ {
		if b[i] == 0 {
			nul++
		}
	}
	if nul > 40 { // mostly NULs in the ASCII range → UTF-16LE
		u := make([]uint16, len(b)/2)
		for i := 0; i+1 < len(b); i += 2 {
			u[i/2] = uint16(b[i]) | uint16(b[i+1])<<8
		}
		return string(utf16.Decode(u))
	}
	return string(b)
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
	case exit == 1638:
		// Another version of this product is already installed — the install did not
		// happen, but it is not a hard failure (idempotency / detection should have
		// caught it; treat as not-applicable).
		res.NotApplicable = true
		res.Reason = "another version of this product is already installed (exit 1638)"
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
func buildInvocation(p Params, abs, logPath string) (bin string, args []string, err error) {
	typ := strings.ToLower(strings.TrimSpace(p.InstallerType))
	if typ == "" || typ == "auto" {
		switch strings.ToLower(filepath.Ext(abs)) {
		case ".msi":
			typ = "msi"
		case ".ps1":
			typ = "ps1"
		case ".bat", ".cmd":
			typ = "bat"
		case ".py":
			typ = "python"
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
		msiArgs := append([]string{flag, abs}, extra...)
		if logPath != "" {
			msiArgs = append(msiArgs, "/l*v", logPath) // verbose log for diagnosis
		}
		return "msiexec.exe", msiArgs, nil
	case "ps1":
		// Executing a .ps1 PAYLOAD the server shipped — a deploy action, not the
		// agent using PowerShell for its own logic (which it never does).
		return "powershell.exe", append([]string{"-NonInteractive", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", abs}, extra...), nil
	case "bat", "cmd":
		return "cmd.exe", append([]string{"/c", abs}, extra...), nil
	case "py", "python":
		// Run a .py PAYLOAD through the Python interpreter (must be on the machine's
		// PATH — the agent runs as LocalSystem). A deploy action, like any script.
		return "python.exe", append([]string{abs}, extra...), nil
	case "exe":
		return abs, extra, nil
	default:
		return "", nil, fmt.Errorf("unknown installer_type %q (want msi|exe|ps1|bat|cmd|python|auto)", p.InstallerType)
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
