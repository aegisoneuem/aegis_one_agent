//go:build !windows

package main

import (
	"os"
	"path/filepath"
)

// Non-Windows fallback: a flag file in the secure dir. The real at-rest key store
// for Linux/macOS (keychain/secret-service) lands with the cross-platform phase.
func keyMarkerPath(secureDir string) string {
	return filepath.Join(secureDir, ".key_provisioned")
}

func platformReadKeyMarker(secureDir string) bool {
	_, err := os.Stat(keyMarkerPath(secureDir))
	return err == nil
}

func platformWriteKeyMarker(secureDir string) error {
	return os.WriteFile(keyMarkerPath(secureDir), []byte("1"), 0o600)
}
