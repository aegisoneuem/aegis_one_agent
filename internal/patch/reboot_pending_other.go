//go:build !windows

package patch

// RebootPending is Windows-only; other platforms report no pending reboot.
func RebootPending() (bool, []string) { return false, nil }
