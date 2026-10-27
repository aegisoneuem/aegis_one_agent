//go:build !windows

package softdist

import (
	"fmt"
	"os"
)

func bundleLocalDestination(string) error               { return nil }
func bundleNoReparse(string) error                      { return nil }
func makeBundleDirectory(path string, _ *Session) error { return os.Mkdir(path, 0700) }
func publishBundle(stage, destination string) error {
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return fmt.Errorf("bundle destination already exists or cannot be inspected")
	}
	return os.Rename(stage, destination)
}

func openBundleArchive(path string) (*os.File, error) { return os.Open(path) }
