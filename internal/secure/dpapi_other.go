//go:build !windows && !linux

package secure

import "errors"

// SealMachine / OpenMachine: Windows = DPAPI, Linux = machine-id-derived AES-GCM.
// macOS/BSD get their native stores (Keychain) later.
func SealMachine([]byte) ([]byte, error) { return nil, errors.New("SealMachine not supported on this platform") }
func OpenMachine([]byte) ([]byte, error) { return nil, errors.New("OpenMachine not supported on this platform") }
