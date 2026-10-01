//go:build windows

package patch

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"
)

const (
	currentVersionKey = `SOFTWARE\Microsoft\Windows NT\CurrentVersion`
	cbsPackagesKey    = `SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\Packages`
)

// CollectLocalState reads the device's OS build and installed KBs from the
// registry — no COM/WUA, no network, milliseconds. This is Step A of the
// recurring patch-detection job: its result is sent to the server (online) or
// queued in the Local Secure Buffer (offline) for a server-side catalog diff.
func CollectLocalState() (*LocalState, error) {
	s := &LocalState{CollectedAt: time.Now()}

	cv, err := registry.OpenKey(registry.LOCAL_MACHINE, currentVersionKey, registry.QUERY_VALUE)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", currentVersionKey, err)
	}
	defer cv.Close()

	s.ProductName, _, _ = cv.GetStringValue("ProductName")
	s.DisplayVersion, _, _ = cv.GetStringValue("DisplayVersion")
	s.BuildNumber, _, _ = cv.GetStringValue("CurrentBuildNumber")
	if ubr, _, err := cv.GetIntegerValue("UBR"); err == nil {
		s.UBR = int(ubr)
	}

	s.FullBuild = s.BuildNumber
	if s.UBR > 0 {
		s.FullBuild = fmt.Sprintf("%s.%d", s.BuildNumber, s.UBR)
	}

	s.Architecture = osArchitecture()

	// ProductName still reports "Windows 10" on Windows 11 (a known registry
	// quirk); correct it from the build number (11 starts at build 22000).
	if parseBuildInt(s.BuildNumber) >= 22000 && strings.Contains(s.ProductName, "Windows 10") {
		s.ProductName = strings.Replace(s.ProductName, "Windows 10", "Windows 11", 1)
	}

	kbs, err := installedKBsFromCBS()
	if err != nil {
		return nil, err
	}
	s.InstalledKBs = kbs

	slog.Debug("local patch state collected",
		"product", s.ProductName, "display_version", s.DisplayVersion,
		"build", s.FullBuild, "installed_kb_count", len(s.InstalledKBs))
	return s, nil
}

// IsKBInstalled reports whether the given KB (with or without the "KB" prefix)
// is already present in the CBS installed-package set. This is a best-effort
// idempotency guard so the agent can skip re-installing a patch that is already
// applied. The OS build is the authoritative cumulative-level signal, so a false
// negative is safe — the install just proceeds, and DISM is itself idempotent.
func IsKBInstalled(kb string) (bool, error) {
	target := normalizeKB(kb)
	if target == "" {
		return false, nil
	}
	kbs, err := installedKBsFromCBS()
	if err != nil {
		return false, err
	}
	for _, k := range kbs {
		if normalizeKB(k) == target {
			return true, nil
		}
	}
	return false, nil
}

// osArchitecture returns the OS architecture. PROCESSOR_ARCHITEW6432 is set when
// a 32-bit process runs on 64-bit Windows (WOW64), so it reflects the true OS
// arch; otherwise PROCESSOR_ARCHITECTURE does.
func osArchitecture() string {
	a := os.Getenv("PROCESSOR_ARCHITEW6432")
	if a == "" {
		a = os.Getenv("PROCESSOR_ARCHITECTURE")
	}
	switch strings.ToUpper(a) {
	case "AMD64":
		return "amd64"
	case "ARM64":
		return "arm64"
	case "X86":
		return "x86"
	default:
		return strings.ToLower(a)
	}
}

// installedKBsFromCBS enumerates the Component Based Servicing package keys and
// extracts the distinct KB numbers. CBS is more complete than WMI's
// Win32_QuickFixEngineering (which misses many CBS-delivered cumulative updates)
// and needs no COM. Still best-effort: the OS build number is the authoritative
// signal for the cumulative level; this KB set supplements it.
func installedKBsFromCBS() ([]string, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, cbsPackagesKey, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", cbsPackagesKey, err)
	}
	defer key.Close()

	names, err := key.ReadSubKeyNames(-1)
	if err != nil {
		return nil, fmt.Errorf("enumerate CBS packages: %w", err)
	}

	set := make(map[string]struct{})
	for _, name := range names {
		if kb := extractKB(name); kb != "" {
			set[kb] = struct{}{}
		}
	}

	out := make([]string, 0, len(set))
	for kb := range set {
		out = append(out, kb)
	}
	sort.Strings(out)
	return out, nil
}
