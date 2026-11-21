//go:build !windows

package config

// Linux/Unix install layout follows the FHS (like BigFix's /opt/BESClient +
// /var/opt/BESClient): the binary lives under /opt/aegisone and all mutable data
// (identity, certs, cache, logs, download) under /var/opt/aegisone — kept out of
// /opt so the data survives a binary-only re-deploy and matches packaging norms.
func defaultInstallDir() string { return "/opt/aegisone" }

func defaultDataDir(_ string) string { return "/var/opt/aegisone" }
