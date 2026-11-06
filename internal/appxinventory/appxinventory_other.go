//go:build !windows

package appxinventory

// Collect is Windows-only; the stub lets the package build on other platforms.
func Collect() ([]AppxPackage, error) {
	return nil, nil
}
