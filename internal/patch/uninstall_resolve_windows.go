//go:build windows

package patch

import (
	"log/slog"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const cbsPackagesPath = `SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\Packages`

// resolvePackageFromRegistry maps a KB to the CBS package DISM must remove, from the
// Component Based Servicing registry — the same source BigFix's uninstall fixlets
// use, and more robust than parsing `dism /get-packages` text.
//
// A cumulative update (LCU) does NOT carry its KB in the package name; instead the
// KB's package key lists the owning Package_for_RollupFix (or, for .NET,
// Package_for_DotNetRollup) under its "Owners" subkey. We take the highest-version
// owner — the current cumulative. A standalone update's own package key name carries
// the KB, so that key is used directly. Returns ("", false) when nothing references
// the KB (the caller then checks the QFE/hotfix list and wusa).
func resolvePackageFromRegistry(kb string) (string, bool) {
	kb = normalizeKB(kb)
	if kb == "" {
		return "", false
	}
	root, err := registry.OpenKey(registry.LOCAL_MACHINE, cbsPackagesPath, registry.ENUMERATE_SUB_KEYS|registry.READ)
	if err != nil {
		slog.Debug("resolve: open CBS Packages failed", "error", err.Error())
		return "", false
	}
	defer root.Close()

	names, err := root.ReadSubKeyNames(-1)
	if err != nil {
		return "", false
	}
	kbToken := "KB" + kb // uppercase compare

	var rollupOwners []string // Package_for_RollupFix / Package_for_DotNetRollup owners
	var standalone []string   // the KB's own package keys (Package_for_KB<n>~...~~<ver>)

	for _, name := range names {
		if !strings.Contains(strings.ToUpper(name), kbToken) {
			continue
		}
		// Standalone candidate: the package key itself names the KB and has a version.
		if strings.HasPrefix(name, "Package_for_") && strings.Contains(name, "~~") {
			standalone = append(standalone, name)
		}
		// Owners of this KB key → the cumulative/rollup package(s) that own it.
		for _, owner := range readPackageOwners(root, name) {
			if strings.HasPrefix(owner, "Package_for_RollupFix") || strings.HasPrefix(owner, "Package_for_DotNetRollup") {
				rollupOwners = append(rollupOwners, owner)
			}
		}
	}

	// The cumulative/.NET rollup owner is the removable package; prefer it.
	if best := maxVersionPackage(rollupOwners); best != "" {
		return best, true
	}
	if best := maxVersionPackage(standalone); best != "" {
		return best, true
	}
	return "", false
}

// readPackageOwners returns the value names under Packages\<pkgKey>\Owners — the
// packages that own (depend on) pkgKey. Empty on any error or missing Owners subkey.
func readPackageOwners(root registry.Key, pkgKey string) []string {
	k, err := registry.OpenKey(root, pkgKey+`\Owners`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	names, err := k.ReadValueNames(-1)
	if err != nil {
		return nil
	}
	return names
}

// maxVersionPackage returns the package identity with the highest trailing version
// (the text after the last "~", compared as dotted integers), or "".
func maxVersionPackage(ids []string) string {
	best, bestVer := "", ""
	for _, id := range ids {
		v := packageVersion(id)
		if best == "" || versionLess(bestVer, v) {
			best, bestVer = id, v
		}
	}
	return best
}

// packageVersion returns the dotted version at the end of a CBS package identity
// (the text after the last "~"), e.g. "20348.587.1.7".
func packageVersion(identity string) string {
	i := strings.LastIndex(identity, "~")
	if i < 0 {
		return ""
	}
	return identity[i+1:]
}

// versionLess reports whether dotted-int version a < b ("20348.587.1.7").
func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var ai, bi int
		if i < len(as) {
			ai = atoiSafe(as[i])
		}
		if i < len(bs) {
			bi = atoiSafe(bs[i])
		}
		if ai != bi {
			return ai < bi
		}
	}
	return false
}

// atoiSafe parses leading digits of s into an int (0 if none); no error path needed
// for version segments.
func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
