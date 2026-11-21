//go:build !windows && !linux

package secure

import "errors"

// NewKeyProvider is not implemented on macOS/BSD yet (Windows = DPAPI, Linux =
// root-only key file). They will use their native secret stores (Keychain) later.
func NewKeyProvider(dir string) KeyProvider { return unsupportedKeyProvider{} }

type unsupportedKeyProvider struct{}

func (unsupportedKeyProvider) Key() ([]byte, error) {
	return nil, errors.New("secure key provider not supported on this OS yet")
}
