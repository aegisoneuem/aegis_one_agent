//go:build !windows

package patch

// CollectInstalledKBs is Windows-only; the stub lets the package build on other
// platforms without pulling in COM.
func CollectInstalledKBs() ([]string, error) {
	return nil, nil
}

// InstalledUpdatesWUA stub for non-Windows builds.
func InstalledUpdatesWUA() ([]string, error) {
	return nil, nil
}
