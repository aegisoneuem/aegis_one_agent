//go:build !windows

package softdist

// SystemOnACPower is Windows-only; non-Windows builds report "unknown" so the
// AC-power gate never blocks on a platform where it cannot measure power.
func SystemOnACPower() (onAC bool, known bool) {
	return false, false
}
