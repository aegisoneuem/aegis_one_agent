//go:build !windows

package secure

import "errors"

// NewKeyProvider is not implemented on non-Windows platforms yet; the agent is
// Windows-first. Linux/macOS will use their native secret stores (e.g. the
// kernel keyring or Keychain) when patch support lands there.
func NewKeyProvider(dir string) KeyProvider { return unsupportedKeyProvider{} }

type unsupportedKeyProvider struct{}

func (unsupportedKeyProvider) Key() ([]byte, error) {
	return nil, errors.New("secure key provider not supported on this OS yet")
}
