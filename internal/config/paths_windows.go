//go:build windows

package config

import (
	"path/filepath"
	"strings"
)

// Windows install layout: binary + data together under
// C:\Program Files\AegisOne\Client, with the data tree in the SKVC subfolder
// (identity, certs, cache, logs, download).
func defaultInstallDir() string { return `C:\Program Files\AegisOne\Client` }

func defaultDataDir(installDir string) string {
	if strings.TrimSpace(installDir) == "" {
		installDir = defaultInstallDir()
	}
	return filepath.Join(strings.TrimRight(installDir, `\/`), "SKVC")
}
