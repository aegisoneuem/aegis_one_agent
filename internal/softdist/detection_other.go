//go:build !windows

package softdist

import "fmt"

// ProductInstalled is Windows-only (MSI product state). Elsewhere it reports
// false, so deploys are not wrongly skipped.
func ProductInstalled(string) bool { return false }

func QueryProductInstalled(string) (bool, error) {
	return false, fmt.Errorf("MSI detection is only supported on Windows")
}

func ResolveInstalledApp(AppSelector) (*InstalledApp, error) {
	return nil, fmt.Errorf("registry detection is only supported on Windows")
}

// FindInstalledApp is Windows-only (registry Uninstall hive). Elsewhere it reports
// no match.
func FindInstalledApp(string, string) *InstalledApp { return nil }
