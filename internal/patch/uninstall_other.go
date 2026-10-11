//go:build !windows

package patch

import "time"

// UninstallKB is Windows-only; other platforms will use their native package
// managers later.
func UninstallKB(kb string, timeout time.Duration) *DeployResult {
	return &DeployResult{Success: false, Reason: "patch uninstall not supported on this OS yet"}
}
