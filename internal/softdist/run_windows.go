//go:build windows

package softdist

import (
	"bytes"
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
	if err := ValidatePrivateInput(p); err != nil {
		return Result{Reason: err.Error()}
	}
	action := strings.ToLower(strings.TrimSpace(p.Action))
	if action != "" && action != "install" && action != "uninstall" {
		return Result{Reason: "invalid software action " + p.Action}
	}
	if _, err := splitWindowsArgs(Substitute(p.SilentArgs, p.Parameters)); err != nil {
		return Result{Reason: "invalid installer arguments: " + err.Error()}
	}
	if isFilelessMSIUninstall(p) {
		if err := validateMSIUninstallArgs(Substitute(p.SilentArgs, p.Parameters)); err != nil {
			return Result{Reason: err.Error()}
		}
		if _, err := windows.GUIDFromString(strings.TrimSpace(p.ProductCode)); err != nil {
			return Result{Reason: "invalid MSI ProductCode: " + err.Error()}
		}
	}

	// Fileless MSI uninstall: when we know the MSI ProductCode and no installer file
	// is present, Windows Installer removes the product from its own database —
	// msiexec /x {GUID}. No file means no integrity/signature check to run (there is
	// nothing to verify); this is the standard-UEM way to uninstall an MSI product.
	if isFilelessMSIUninstall(p) {
		pc := strings.TrimSpace(p.ProductCode)
		logPath := ""
		if !p.sensitive() {
			logPath = filepath.Join(os.TempDir(), "aegis-uninstall-"+sanitizeCode(pc)+".log")
		}
		bin, args := msiUninstallByCode(p, pc, logPath)
		return execInstaller(ctx, p, bin, args, logPath)
	}

	// Fileless registry uninstall: a non-MSI product is removed by running the
	// command the vendor registered in the Windows Uninstall hive — no installer file.
	if isFilelessRegistryUninstall(p) {
		return runRegistryUninstall(ctx, p)
	}

	// Generic "command" type: run ANY command the admin specifies — a system tool
	// (netsh, reg), an interpreter (java -jar ...), choco, etc. No file is required
	// and no delivery/hash/signature is enforced (the admin owns the command). This is
	// the no-limit escape hatch for software types without a dedicated handler.
	if strings.EqualFold(strings.TrimSpace(p.InstallerType), "command") {
		bin := Substitute(strings.TrimSpace(p.FilePath), p.Parameters)
		if bin == "" {
			return Result{Reason: `installer_type "command" requires file_path to be the command to run`}
		}
		args, err := splitWindowsArgs(Substitute(p.SilentArgs, p.Parameters))
		if err != nil {
			return Result{Reason: "invalid command arguments: " + err.Error()}
		}
		return execInstaller(ctx, p, bin, args, "")
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

	// Integrity (MANDATORY for every file-based payload, BigFix prefetch model): the
	// file on disk is trusted only if it matches the hash pinned in the signed
	// command. The download folder is untrusted staging — a payload without a pinned
	// hash, or with a mismatch, is refused outright. Fail loud, no bypass.
	want := strings.TrimSpace(p.ExpectedHash)
	if want == "" {
		return Result{Reason: "unverified payload refused — expected_hash is required for file-based deploys"}
	}
	got, herr := fileHash(abs, p.HashAlgorithm)
	if herr != nil {
		return Result{Reason: "could not hash installer for integrity check", Detail: herr.Error()}
	}
	if !strings.EqualFold(got, want) {
		slog.Error("softdist: installer integrity check FAILED — refusing to run",
			"file", abs, "expected", want, "actual", got)
		return Result{Reason: "integrity check failed (hash mismatch) — refusing to run"}
	}
	slog.Info("softdist: installer integrity verified", "file", abs)

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
	if p.sensitive() {
		logPath = ""
	}
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
	extra, _ := splitWindowsArgs(Substitute(p.SilentArgs, p.Parameters))
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
	if strings.EqualFold(strings.TrimSpace(p.InstallerType), "command") {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(p.Action), "uninstall") {
		return false
	}
	if strings.TrimSpace(p.FilePath) != "" || strings.TrimSpace(p.ProductCode) != "" {
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
	selector := AppSelector{UninstallKey: p.UninstallKey, DisplayName: p.DisplayName, Version: p.Version}
	if p.Session != nil {
		selector.UserSID = p.Session.DetectionSID()
	}
	app, err := ResolveInstalledApp(selector)
	if err != nil {
		return Result{Reason: "uninstall detection failed: " + err.Error()}
	}
	if app == nil {
		return Result{NotApplicable: true, Reason: "product not installed (nothing to uninstall)"}
	}
	logPath := ""
	// MSI planner uses a per-run unique diagnostic file; vendor EXEs do not need it.
	if !p.sensitive() {
		f, err := os.CreateTemp("", "aegis-uninstall-*.log")
		if err != nil {
			return Result{Reason: "create uninstall log failed"}
		}
		logPath = f.Name()
		f.Close()
		defer os.Remove(logPath)
	}
	bin, args, err := registryUninstallInvocation(p, app, logPath)
	if err != nil {
		return Result{Reason: err.Error()}
	}
	slog.Info("softdist: uninstalling registry product", "name", app.DisplayName, "key", app.Key, "view", app.RegistryView, "version", app.DisplayVersion)
	return execInstaller(ctx, p, bin, args, logPath)
}

// commandLineToArgv splits a Windows command-line string into argv using the OS's own
// CommandLineToArgvW (shell32), so quoted
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
	// An explicit cmd.exe command owns its /C shell text, including quotes
	// adjacent to redirection operators. Splitting and re-encoding it as argv
	// changes that text (notably paths containing spaces). Preserve the signed
	// author's raw arguments; all other executables keep normal argv handling.
	if strings.EqualFold(strings.TrimSpace(p.InstallerType), "command") && strings.EqualFold(filepath.Base(bin), "cmd.exe") {
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: syscall.EscapeArg(cmd.Path) + " " + Substitute(p.SilentArgs, p.Parameters), CreationFlags: 0x08000000}
	}
	// cmd.exe parses /C text itself; Go's normal argv encoding cannot preserve a
	// batch filename with spaces inside that command text. Keep the script path
	// explicitly quoted and preserve the package author's Windows CMD arguments.
	if !strings.EqualFold(strings.TrimSpace(p.InstallerType), "command") &&
		strings.EqualFold(filepath.Base(bin), "cmd.exe") && len(args) >= 2 && strings.EqualFold(args[0], "/c") &&
		(strings.EqualFold(filepath.Ext(args[1]), ".bat") || strings.EqualFold(filepath.Ext(args[1]), ".cmd")) {
		text := `"` + args[1] + `"`
		if extra := Substitute(p.SilentArgs, p.Parameters); strings.TrimSpace(extra) != "" {
			text += " " + extra
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /D /S /C "` + text + `"`, CreationFlags: 0x08000000}
	}
	if p.Session != nil {
		if err := p.Session.Prepare(cmd); err != nil {
			return Result{Reason: "prepare execution context failed"}
		}
	}
	if p.WorkingDirectory != "" {
		cmd.Dir = p.WorkingDirectory
	}
	// Export parameters to child scripts as AEGIS_PARAM_<NAME> env vars (in addition
	// to ${name} substitution in the args), so a .ps1/.py/.bat payload can read them.
	if env := paramEnv(p.Parameters); len(env) > 0 {
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = append(cmd.Env, env...)
	}
	if len(p.SecureParameters) > 0 {
		input, err := secretInput(p.SecureParameters)
		if err != nil {
			return Result{Reason: "encode private installer input failed"}
		}
		defer clear(input)
		cmd.Stdin = bytes.NewReader(input)
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = append(cmd.Env, "AEGIS_SECRET_INPUT=stdin-json-v1")
	}
	invokeStart := time.Now()
	// Descendant termination: wrap the installer in a Windows Job Object so a
	// timeout, cancel or agent crash kills every child (msiexec engines, EXE
	// post-install helpers, batch chains). Without this, Context-cancel only
	// kills the top-level process and orphaned installers survive.
	job, err := newJobGroup(p.Name)
	if err != nil {
		slog.Warn("softdist: job object setup failed, continuing without descendant guard", "error", err.Error())
	}
	defer func() {
		if job != nil {
			_ = job.Close() // KILL_ON_JOB_CLOSE sweeps any remaining descendants
		}
	}()

	var out []byte
	var runErr error
	if p.sensitive() {
		slog.Info("softdist: running private installer", "timeout", timeout.String())
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if runErr = cmd.Start(); runErr == nil {
			if job != nil {
				if err := job.Attach(cmd); err != nil {
					slog.Warn("softdist: job object attach failed", "pid", cmd.Process.Pid, "error", err.Error())
				}
			}
			runErr = cmd.Wait()
		}
	} else {
		slog.Info("softdist: running installer", "name", p.Name, "bin", bin, "args", strings.Join(args, " "), "timeout", timeout.String())
		pr, pw, pipeErr := os.Pipe()
		if pipeErr != nil {
			return Result{Reason: "pipe creation failed: " + pipeErr.Error()}
		}
		cmd.Stdout = pw
		cmd.Stderr = pw
		if runErr = cmd.Start(); runErr == nil {
			if job != nil {
				if err := job.Attach(cmd); err != nil {
					slog.Warn("softdist: job object attach failed", "pid", cmd.Process.Pid, "error", err.Error())
				}
			}
			pw.Close()
			out, _ = io.ReadAll(pr)
			runErr = cmd.Wait()
		} else {
			pw.Close()
		}
		pr.Close()
	}

	// Prefer the MSI log tail over the (empty) console output for Detail.
	detail := tail(string(out), 600)
	if p.sensitive() {
		detail = "installer output suppressed for private deployment"
	}
	if logPath != "" && !p.sensitive() {
		if logTail := readInstallLog(logPath); logTail != "" {
			detail = logTail
		}
		_ = os.Remove(logPath)
	}

	elapsed := time.Since(invokeStart)
	if rctx.Err() == context.Canceled {
		return Result{Reason: fmt.Sprintf("installer cancelled after %s; already-started child operations may still be running%s", elapsed.Round(time.Second), describeDiagnostics(job)), Detail: detail}
	}
	if rctx.Err() == context.DeadlineExceeded {
		return Result{Reason: fmt.Sprintf("installer timed out after %s%s", timeout, describeDiagnostics(job)), Detail: detail}
	}
	if cmd.ProcessState == nil {
		// The installer failed to even start (missing binary, bad path, etc.).
		if p.sensitive() {
			return Result{Reason: "could not start private installer", Detail: detail}
		}
		return Result{Reason: "could not start installer", Detail: fmt.Sprintf("%v", runErr)}
	}
	res := classify(p, cmd.ProcessState.ExitCode(), string(out))
	res.Detail = detail
	return res
}

// describeDiagnostics appends causal signals that help root-cause isolation
// when an installer times out or is cancelled. The Firefox / MSI acceptance
// gap flagged in memory ("original MSI timeout and missing post-step root
// causes not isolated") was invisible without this: now a timeout reason
// carries "2 descendants still alive at kill" vs "0 descendants" so the
// operator can tell a stuck parent apart from stuck children. The signal is
// best-effort — a kernel race may make the count 0 even when children were
// present a millisecond earlier; this is still much better than silence.
func describeDiagnostics(job *jobGroup) string {
	if job == nil {
		return ""
	}
	n := job.activeDescendants()
	if n == 0 {
		return "; no descendants were alive at kill (parent process was likely stuck)"
	}
	return fmt.Sprintf("; %d descendant process(es) killed with parent (child installer helpers were still running)", n)
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
		verb := "installed"
		if strings.EqualFold(strings.TrimSpace(p.Action), "uninstall") {
			verb = "uninstalled"
		}
		if res.RebootRequired {
			res.Reason = verb + " — RESTART REQUIRED to complete"
		} else {
			res.Reason = verb + " — completed, no restart needed"
		}
		return res
	}
	switch {
	case exit == 1605 && strings.EqualFold(strings.TrimSpace(p.Action), "uninstall"):
		res.NotApplicable = true
		res.Reason = "product not installed (nothing to uninstall)"
	case exit == 1638 && !strings.EqualFold(strings.TrimSpace(p.Action), "uninstall"):
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
		case ".vbs":
			typ = "vbs"
		default:
			typ = "exe"
		}
	}
	// Substitute ${name} parameters into the silent args, then split.
	// Windows argument parsing preserves quoted paths and property values. The installer PATH is
	// passed as its own arg (not via a shell), so spaces in the path are safe.
	extra, parseErr := splitWindowsArgs(Substitute(p.SilentArgs, p.Parameters))
	if parseErr != nil {
		return "", nil, fmt.Errorf("invalid installer arguments: %w", parseErr)
	}
	uninstall := strings.EqualFold(strings.TrimSpace(p.Action), "uninstall")

	switch typ {
	case "msi":
		flag := "/i"
		if uninstall {
			if err := validateMSIUninstallArgs(Substitute(p.SilentArgs, p.Parameters)); err != nil {
				return "", nil, err
			}
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
	case "vbs":
		// VBScript payload via the console host (cscript), no banner.
		return "cscript.exe", append([]string{"//nologo", "//B", abs}, extra...), nil
	case "exe":
		return abs, extra, nil
	default:
		return "", nil, fmt.Errorf("unknown installer_type %q (want msi|exe|ps1|bat|cmd|python|vbs|command|auto)", p.InstallerType)
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
