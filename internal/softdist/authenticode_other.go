//go:build !windows

package softdist

import "errors"

// verifyAuthenticode is Windows-only (WinVerifyTrust). On other platforms the
// publisher check is unavailable, so requiring a signature fails closed.
func verifyAuthenticode(string) error {
	return errors.New("Authenticode verification is only supported on Windows")
}
