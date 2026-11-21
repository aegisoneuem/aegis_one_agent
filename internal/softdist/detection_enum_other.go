//go:build !windows && !linux

package softdist

// EnumerateInstalledApps: Windows = registry Uninstall hive, Linux = dpkg/rpm.
// macOS/BSD report an empty inventory for now (so cross-compiles/vet pass).
func EnumerateInstalledApps() ([]InstalledApp, error) { return nil, nil }
