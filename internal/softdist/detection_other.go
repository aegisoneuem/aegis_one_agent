//go:build !windows

package softdist

import "fmt"

// ProductInstalled is Windows-only (MSI product state). Elsewhere it reports
// false, so deploys are not wrongly skipped.
func ProductInstalled(string) bool { return false }

func QueryProductInstalled(string) (bool, error) {
	return false, fmt.Errorf("MSI detection is only supported on Windows")
}

func QueryProductInScope(string, string) (bool, string, error) {
	return false, "", fmt.Errorf("MSI scoped detection requires Windows")
}

func ResolveInstalledApp(AppSelector) (*InstalledApp, error) {
	return nil, fmt.Errorf("registry detection is only supported on Windows")
}

// FindInstalledApp is Windows-only (registry Uninstall hive). Elsewhere it reports
// no match.
func FindInstalledApp(string, string) *InstalledApp { return nil }

// EnumerateInstalledApps is Windows-only (registry Uninstall hive). Non-Windows
// agents report an empty inventory rather than an error so DeviceStateReport
// still goes out when the agent is cross-compiled for CI / vet.
func EnumerateInstalledApps() ([]InstalledApp, error) { return nil, nil }
