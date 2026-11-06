//go:build windows

package appxinventory

import (
	"fmt"
	"log/slog"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	// stateRepoPackageData is the SYSTEM-WIDE package table the OS's own AppX
	// APIs read — the authoritative list of EVERY installed package (all users
	// + provisioned + system), readable by the LocalSystem agent without
	// walking per-user hives. This is why we do not need PowerShell's
	// Get-AppxPackage (which BigFix fell back to).
	stateRepoPackageData = `SOFTWARE\Microsoft\Windows\CurrentVersion\AppModel\StateRepository\Cache\Package\Data`
	// userRepoPackages (under each HKU\<SID>) holds the friendly DisplayName.
	userRepoPackages = `Software\Classes\Local Settings\Software\Microsoft\Windows\CurrentVersion\AppModel\Repository\Packages`
)

// Collect enumerates every installed MSIX/AppX/Store package from the system
// StateRepository and enriches each with a friendly DisplayName from the
// per-user repositories where available. Pure registry — no PowerShell, no
// COM/WinRT, no network. Fail-loud: a failure to open the authoritative table
// returns an error (unknown package set is not "no packages").
func Collect() ([]AppxPackage, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, stateRepoPackageData, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil, fmt.Errorf("appx: open StateRepository Package\\Data: %w", err)
	}
	defer key.Close()
	ids, err := key.ReadSubKeyNames(-1)
	if err != nil {
		return nil, fmt.Errorf("appx: enumerate packages: %w", err)
	}

	displayNames := collectDisplayNames() // best-effort

	raw := make([]AppxPackage, 0, len(ids))
	for _, id := range ids {
		sub, err := registry.OpenKey(registry.LOCAL_MACHINE, stateRepoPackageData+`\`+id, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		full, _, _ := sub.GetStringValue("PackageFullName")
		loc, _, _ := sub.GetStringValue("InstalledLocation")
		origin, _, _ := sub.GetIntegerValue("PackageOrigin")
		sub.Close()
		if full == "" || isResourceSatellite(full) {
			continue // skip resource/language satellite splits (noise)
		}
		pkg, ok := buildPackage(full, loc, int64(origin))
		if !ok {
			continue
		}
		if dn := displayNames[full]; dn != "" {
			pkg.DisplayName = dn
		}
		raw = append(raw, pkg)
	}

	out := dedupePackages(raw) // collapse bundle/arch rows to one per app
	slog.Debug("appx inventory collected", "packages", len(out))
	return out, nil
}

// collectDisplayNames walks the loaded user hives (HKU\<SID>) and maps each
// PackageFullName to its friendly DisplayName. Best-effort: SIDs without the
// repository (service accounts, _Classes hives) are skipped, and any read error
// is ignored — a missing DisplayName just leaves the field empty (the server
// can fall back to the PackageFamilyName).
func collectDisplayNames() map[string]string {
	out := make(map[string]string)
	users, err := registry.OpenKey(registry.USERS, "", registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return out
	}
	defer users.Close()
	sids, err := users.ReadSubKeyNames(-1)
	if err != nil {
		return out
	}
	for _, sid := range sids {
		if strings.HasSuffix(sid, "_Classes") {
			continue // the paired Classes hive, not a user profile
		}
		path := sid + `\` + userRepoPackages
		pkgsKey, err := registry.OpenKey(registry.USERS, path, registry.ENUMERATE_SUB_KEYS)
		if err != nil {
			continue
		}
		names, err := pkgsKey.ReadSubKeyNames(-1)
		pkgsKey.Close()
		if err != nil {
			continue
		}
		for _, full := range names {
			if _, seen := out[full]; seen {
				continue
			}
			pk, err := registry.OpenKey(registry.USERS, path+`\`+full, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			dn, _, _ := pk.GetStringValue("DisplayName")
			pk.Close()
			if dn != "" {
				out[full] = dn
			}
		}
	}
	return out
}
