//go:build !windows

package secure

import "errors"

// SealMachine / OpenMachine are Windows-only (machine-scope DPAPI). The
// Linux/macOS equivalents for critical-key protection (Secret Service / Keychain)
// land with the cross-platform phase. The agent's key provider is already
// unsupported off-Windows, so these are never reached in a working agent today.
func SealMachine([]byte) ([]byte, error) { return nil, errors.New("SealMachine not supported on this platform") }
func OpenMachine([]byte) ([]byte, error) { return nil, errors.New("OpenMachine not supported on this platform") }
