package secure

// KeyFileName is the on-disk name of the DPAPI-protected master key blob, stored
// under the agent's secure directory. Exported so the agent can check the key's
// integrity at startup (tamper/delete detection) without re-deriving the path.
const KeyFileName = "dpapi.key"

// KeyProvider supplies the 32-byte AES-256 master key used to encrypt the
// agent's data at rest. The Windows implementation protects the key with DPAPI
// (machine scope); other platforms will get their own secure store later.
//
// Key() is safe to call repeatedly — implementations cache the key in memory
// after the first load so the DPAPI round-trip happens only once.
type KeyProvider interface {
	Key() ([]byte, error)
}
