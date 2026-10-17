//go:build windows

package patch

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/logging"
)

// cbsPackage is one row of `dism /online /get-packages` — a package in the CBS
// (Component-Based Servicing) store with its state.
type cbsPackage struct {
	Identity string // e.g. "Package_for_KB5027397~31bf3856ad364e35~amd64~~22621.2355.1.1"
	State    string // "Installed" | "Superseded" | "Install Pending" | ...
}

// UninstallKBviaDISM removes an installed update at the CBS level — the same
// servicing engine Control Panel uses, and the ONLY silent removal front-end left
// on modern Windows (wusa's silent uninstall was removed by Microsoft; it returns
// ERROR_INVALID_PARAMETER in session 0). It is also the path that can remove
// cumulative updates (LCUs), which WUA's Uninstall cannot.
//
// Matching: a standalone update's CBS package carries the KB in its identity
// (Package_for_KB<n>~...). A cumulative update's package does NOT — it carries the
// OS build (Package_for_RollupFix~...~<build>...), so the server supplies osBuild
// from its catalog to match it.
//
// Safety: removal goes through CBS/TrustedInstaller, which is transactional and
// dependency-aware — a package that is permanent (e.g. a servicing stack update) or
// depended upon is REFUSED by Windows, never half-removed. We never force past a
// refusal. A Superseded package is reported as not removable (a newer update
// replaced it) without attempting anything.
//
// Returns (result, attempted). attempted=false means the KB has no matching CBS
// package on this machine — the caller should fall back to the WUA path.
func UninstallKBviaDISM(kb, osBuild string, timeout time.Duration) (*DeployResult, bool) {
	kb = normalizeKB(kb)
	if kb == "" {
		return &DeployResult{Reason: "no KB specified for uninstall"}, true
	}

	// PRIMARY: resolve the CBS package from the registry (KB → Owners → RollupFix /
	// DotNetRollup, or the standalone Package_for_KB key). This finds a cumulative
	// update's package even though its name carries no KB, and needs no os_build.
	pkg, found := resolvePackageFromRegistry(kb)

	// SECONDARY: if the registry lookup found nothing and the server supplied the OS
	// build, match the RollupFix package from the DISM package list by that build.
	if !found && strings.TrimSpace(osBuild) != "" {
		if pkgs, err := listCBSPackages(timeout); err == nil {
			if p, ok := matchCBSPackage(pkgs, kb, osBuild); ok {
				pkg, found = p.Identity, true
			}
		}
	}

	if !found {
		slog.Info("deploy: no CBS package references this KB — not DISM-removable (will check QFE/wusa)", "kb", "KB"+kb)
		return nil, false
	}

	slog.Info("deploy: uninstalling via DISM /remove-package (CBS)", "kb", "KB"+kb, "package", pkg)
	return removeCBSPackage(pkg, timeout), true
}

// listCBSPackages enumerates the CBS package store via
// `dism /online /get-packages /format:table` (read-only) and parses the table.
func listCBSPackages(timeout time.Duration) ([]cbsPackage, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "dism.exe", "/online", "/get-packages", "/format:table")
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("DISM get-packages timed out after %s", timeout)
	}
	if cmd.ProcessState == nil {
		return nil, fmt.Errorf("could not run DISM: %w", err)
	}
	if code := cmd.ProcessState.ExitCode(); code != 0 {
		return nil, fmt.Errorf("DISM get-packages failed (exit %d): %s", code, tail(string(out), 300))
	}
	return parseCBSPackageTable(string(out)), nil
}

// parseCBSPackageTable extracts {Identity, State} rows from DISM's /format:table
// output. Rows look like:
//
//	Package_for_KB5027397~31bf...~amd64~~22621.2355.1.1 | Installed | Update | 04-12-2023 06:15
func parseCBSPackageTable(out string) []cbsPackage {
	var pkgs []cbsPackage
	for _, line := range strings.Split(out, "\n") {
		cols := strings.Split(line, "|")
		if len(cols) < 2 {
			continue
		}
		identity := strings.TrimSpace(cols[0])
		state := strings.TrimSpace(cols[1])
		// Skip the header and separator rows; real identities contain '~'.
		if identity == "" || !strings.Contains(identity, "~") {
			continue
		}
		pkgs = append(pkgs, cbsPackage{Identity: identity, State: state})
	}
	return pkgs
}

// matchCBSPackage finds the CBS package for a KB: first a standalone package that
// names the KB (Package_for_KB<n>~...), else — when the server supplied the OS
// build — the cumulative update package for that build
// (Package_for_RollupFix~...~<build>...). Installed packages win over Superseded
// ones when both match (e.g. an LCU chain).
func matchCBSPackage(pkgs []cbsPackage, kb, osBuild string) (cbsPackage, bool) {
	kbPrefix := "package_for_kb" + kb + "~"
	build := strings.TrimSpace(osBuild)
	var fallback *cbsPackage
	for i := range pkgs {
		p := &pkgs[i]
		id := strings.ToLower(p.Identity)
		match := strings.HasPrefix(id, kbPrefix)
		if !match && build != "" && strings.HasPrefix(id, "package_for_rollupfix~") &&
			strings.Contains(id, "~"+strings.ToLower(build)+".") {
			match = true
		}
		if !match {
			continue
		}
		if strings.EqualFold(p.State, "Installed") {
			return *p, true
		}
		if fallback == nil {
			fallback = p
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	return cbsPackage{}, false
}

// removeCBSPackage runs `dism /online /remove-package /packagename:<id> /quiet
// /norestart`. CBS is transactional: a refusal (permanent package, dependency)
// returns an error code with the system unchanged — we report it honestly and
// NEVER force.
func removeCBSPackage(identity string, timeout time.Duration) *DeployResult {
	if timeout <= 0 {
		timeout = installTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "dism.exe",
		"/online", "/remove-package",
		"/packagename:"+identity,
		"/quiet", "/norestart")
	slog.Debug("deploy: running DISM remove", "command", strings.Join(cmd.Args, " "))
	stop := startHeartbeat("DISM uninstall")
	out, runErr := cmd.CombinedOutput()
	stop()
	logging.Trace("deploy: DISM remove full output", "package", identity, "output", strings.TrimSpace(string(out)))

	if ctx.Err() == context.DeadlineExceeded {
		return &DeployResult{Reason: fmt.Sprintf("DISM remove timed out after %s", timeout)}
	}
	if cmd.ProcessState == nil {
		return &DeployResult{Reason: "could not run DISM", Detail: fmt.Sprintf("%v", runErr)}
	}

	exitCode := cmd.ProcessState.ExitCode()
	res := &DeployResult{ExitCode: exitCode, Detail: tail(string(out), 600)}
	switch exitCode {
	case 0:
		res.Success = true
	case 3010, 1641:
		res.Success = true
		res.RebootRequired = true
	default:
		res.Reason = classifyDISMRemoveFailure(exitCode, string(out))
	}
	if res.Success {
		slog.Info("deploy: DISM uninstall succeeded", "package", identity, "exit_code", exitCode, "reboot_required", res.RebootRequired)
	} else {
		slog.Error("deploy: DISM uninstall FAILED", "package", identity, "exit_code", exitCode, "reason", res.Reason)
	}
	return res
}

// classifyDISMRemoveFailure maps a DISM /remove-package failure to a concise human
// cause. The key case: CBS REFUSING a removal (permanent package / dependency) is
// Windows protecting itself — reported as such, never forced.
func classifyDISMRemoveFailure(exitCode int, output string) string {
	o := strings.ToLower(output)
	switch {
	case strings.Contains(o, "0x800f0825") || strings.Contains(o, "permanent"):
		return "Windows refuses to remove this package — it is permanent (e.g. a servicing stack update or baked into the OS image)"
	case strings.Contains(o, "0x800f0805"):
		return "package not found in the CBS store (already removed?)"
	case strings.Contains(o, "0x800f0904"):
		return "removal blocked — a pending servicing operation requires a reboot first"
	case strings.Contains(o, "0x80070005") || strings.Contains(o, "access is denied"):
		return "access denied — the agent must run elevated (LocalSystem) to uninstall"
	case strings.Contains(o, "0x80070020") || strings.Contains(o, "another installation is") || strings.Contains(o, "in progress"):
		return "servicing stack busy / another operation in progress (retry)"
	case strings.Contains(o, "trustedinstaller") && (strings.Contains(o, "disabled") || strings.Contains(o, "cannot be started")):
		return "Windows servicing service (TrustedInstaller) is disabled or cannot start"
	}
	if h := firstHRESULT(output); h != "" {
		return fmt.Sprintf("uninstall failed (exit %d, %s)", exitCode, h)
	}
	return fmt.Sprintf("uninstall failed (exit %d)", exitCode)
}
