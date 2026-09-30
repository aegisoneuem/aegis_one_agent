// Package secure provides the agent's data-at-rest protection: an AES-256-GCM
// seal/open helper and a platform key provider that guards the master key
// (Windows uses DPAPI, machine scope). Callers encrypt buffered records and
// cached patch files with the master key; the key itself is never written in
// plaintext.
package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// KeySize is the AES-256 master key length in bytes.
const KeySize = 32

// Seal encrypts plaintext with AES-256-GCM under key and returns
// nonce||ciphertext||tag. A fresh random nonce is used per call. GCM also
// authenticates the data, so any tampering is detected on Open.
func Seal(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	// Seal appends ciphertext+tag to the nonce slice, so the nonce is stored
	// as the prefix of the returned blob.
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Open reverses Seal. It fails loud if the blob is truncated or fails the GCM
// authentication check (i.e. it was tampered with or encrypted under a
// different key).
func Open(key, blob []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(blob) < ns {
		return nil, errors.New("ciphertext too short to contain a nonce")
	}
	nonce, ciphertext := blob[:ns], blob[ns:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt/authenticate failed (tampered or wrong key): %w", err)
	}
	return plaintext, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new AES cipher: %w", err)
	}
	return cipher.NewGCM(block)
}
