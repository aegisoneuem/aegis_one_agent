// Package appxinventory collects installed MSIX / AppX / Microsoft Store
// packages — the modern-app software surface that the classic registry
// Uninstall hive (appwiz.cpl / DeviceStateReport.installed_apps) does NOT
// show. The server matches these against its third-party catalog to detect
// outdated / vulnerable Store apps and drive updates, and to complete the
// security software inventory. The agent reports facts only.
//
// Windows-first (appxinventory_windows.go) with a no-op stub elsewhere. Pure
// registry reads (no PowerShell — unlike BigFix's Get-AppxPackage — no COM,
// no WinRT), using the system-wide StateRepository the LocalSystem agent can
// read for ALL users, enriched with per-user DisplayName where available.
package appxinventory

import (
	"sort"
	"strings"
)

// AppxPackage is one installed modern-app package. PackageFullName is the
// stable identity (encodes name/version/arch/publisher); PackageFamilyName
// (Name_PublisherId) is the server catalog's match key.
type AppxPackage struct {
	DisplayName       string // friendly name ("Microsoft Photos"); best-effort, may be ""
	PackageFullName   string // "Name_Version_Arch_ResourceId_PublisherId"
	PackageFamilyName string // "Name_PublisherId" — catalog match key
	Name              string // parsed from full name
	Version           string // parsed, e.g. "0.20.63.0"
	Architecture      string // "x64" | "x86" | "arm64" | "neutral"
	PublisherID       string // publisher hash, e.g. "8wekyb3d8bbwe" (Microsoft)
	InstallLocation   string
	Origin            string // "Store" | "Inbox" | "LineOfBusiness" | "DeveloperSigned" | ... | "Unknown"
}

// parsePackageFullName splits a PackageFullName into its parts. The format is
// Name_Version_Architecture_ResourceId_PublisherId. A package Name never
// contains "_" (it is the delimiter), but to be safe we split from the RIGHT:
// the last four "_"-separated segments are fixed (publisher, resource, arch,
// version) and everything before them is the name. Returns empty strings when
// the input does not have the expected shape.
func parsePackageFullName(full string) (name, version, arch, resourceID, publisherID string) {
	full = strings.TrimSpace(full)
	parts := strings.Split(full, "_")
	if len(parts) < 5 {
		return "", "", "", "", ""
	}
	n := len(parts)
	publisherID = parts[n-1]
	resourceID = parts[n-2]
	arch = parts[n-3]
	version = parts[n-4]
	name = strings.Join(parts[:n-4], "_")
	return name, version, arch, resourceID, publisherID
}

// dedupePackages collapses the multiple StateRepository rows a single installed
// app produces (e.g. a "neutral" bundle entry plus an "x64" arch entry of the
// SAME name+version) into one row per app, and returns the set sorted by full
// name. The key is PackageFamilyName+version (or the full name when the family
// could not be parsed). Among rows with the same key the "best" is kept: one
// with a DisplayName wins, then a concrete architecture over "neutral".
func dedupePackages(in []AppxPackage) []AppxPackage {
	best := make(map[string]AppxPackage, len(in))
	for _, p := range in {
		key := p.PackageFamilyName + "|" + p.Version
		if p.PackageFamilyName == "" {
			key = p.PackageFullName
		}
		if cur, ok := best[key]; !ok || preferAppx(p, cur) {
			best[key] = p
		}
	}
	out := make([]AppxPackage, 0, len(best))
	for _, p := range best {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PackageFullName < out[j].PackageFullName })
	return out
}

// preferAppx reports whether candidate a is a better canonical row than b for
// the same app: a DisplayName is the strongest signal (that is the real app
// entry, not a bundle stub), then a concrete architecture over "neutral".
func preferAppx(a, b AppxPackage) bool {
	if (a.DisplayName != "") != (b.DisplayName != "") {
		return a.DisplayName != ""
	}
	if (a.Architecture == "neutral") != (b.Architecture == "neutral") {
		return a.Architecture != "neutral"
	}
	return false
}

// isResourceSatellite reports whether a PackageFullName is a resource/satellite
// package (a per-scale or per-language split of a main app, e.g. resourceId
// "split.scale-125" / "split.language-en-us"). These share the main app's
// name+version, update WITH it, and are pure noise for software inventory /
// patching — so they are dropped, leaving one row per real app. A main package
// has an empty or "neutral" resourceId.
func isResourceSatellite(fullName string) bool {
	_, _, _, resourceID, _ := parsePackageFullName(fullName)
	return strings.HasPrefix(resourceID, "split.")
}

// packageFamilyName builds the PackageFamilyName (Name_PublisherId), the stable
// key the server catalog matches on. Empty when either part is missing.
func packageFamilyName(name, publisherID string) string {
	if name == "" || publisherID == "" {
		return ""
	}
	return name + "_" + publisherID
}

// originString maps the StateRepository PackageOrigin integer to a readable
// classification. Values per the AppModel PackageOrigin enum.
func originString(origin int64) string {
	switch origin {
	case 1:
		return "Unsigned"
	case 2:
		return "Inbox" // shipped with Windows
	case 3:
		return "Store"
	case 4:
		return "DeveloperUnsigned"
	case 5:
		return "DeveloperSigned"
	case 6:
		return "LineOfBusiness"
	default:
		return "Unknown"
	}
}

// buildPackage assembles an AppxPackage from a PackageFullName + install
// location + origin, parsing the identity parts. Returns ok=false when the
// full name is unparseable (so the caller drops the noise rather than report
// a package with no identity).
func buildPackage(fullName, installLocation string, origin int64) (AppxPackage, bool) {
	name, version, arch, _, pubID := parsePackageFullName(fullName)
	if name == "" {
		return AppxPackage{}, false
	}
	return AppxPackage{
		PackageFullName:   fullName,
		PackageFamilyName: packageFamilyName(name, pubID),
		Name:              name,
		Version:           version,
		Architecture:      arch,
		PublisherID:       pubID,
		InstallLocation:   installLocation,
		Origin:            originString(origin),
	}, true
}
