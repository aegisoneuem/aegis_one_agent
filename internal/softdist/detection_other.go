//go:build !windows

package softdist

// ProductInstalled is Windows-only (MSI product state). Elsewhere it reports
// false, so deploys are not wrongly skipped.
func ProductInstalled(string) bool { return false }

// InstalledApp mirrors the Windows registry Uninstall-hive record (see the Windows
// build); defined here so cross-platform code compiles.
type InstalledApp struct {
	Key                  string
	DisplayName          string
	DisplayVersion       string
	Publisher            string
	UninstallString      string
	QuietUninstallString string
}

// FindInstalledApp is Windows-only (registry Uninstall hive). Elsewhere it reports
// no match.
func FindInstalledApp(string, string) *InstalledApp { return nil }
