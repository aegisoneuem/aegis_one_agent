//go:build !windows

package patch

// CollectDrivers is Windows-only; the stub lets the package build on other
// platforms (dev machines, CI) without pulling in COM.
func CollectDrivers() ([]DriverInfo, error) {
	return nil, nil
}
