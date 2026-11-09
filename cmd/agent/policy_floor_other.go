//go:build !windows

package main

import (
	"os"
	"path/filepath"
)

// On non-Windows the sealed floor is a file under the secure dir (there is no
// HKLM registry). Tests on any platform exercise this path.
func floorFilePath(secureDir string) string {
	return filepath.Join(secureDir, "policy.floor")
}

func platformReadFloorBlob(secureDir string) ([]byte, error) {
	b, err := os.ReadFile(floorFilePath(secureDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	return b, err
}

func platformWriteFloorBlob(secureDir string, blob []byte) error {
	if err := os.MkdirAll(secureDir, 0o700); err != nil {
		return err
	}
	path := floorFilePath(secureDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
