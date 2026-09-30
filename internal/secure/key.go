package secure

// KeyProvider supplies the 32-byte AES-256 master key used to encrypt the
// agent's data at rest. The Windows implementation protects the key with DPAPI
// (machine scope); other platforms will get their own secure store later.
//
// Key() is safe to call repeatedly — implementations cache the key in memory
// after the first load so the DPAPI round-trip happens only once.
type KeyProvider interface {
	Key() ([]byte, error)
}
