//go:build linux

package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"strings"
)

// SealMachine / OpenMachine on Linux: the Windows build uses machine-scope DPAPI; here
// we AES-256-GCM the data with a key derived from /etc/machine-id (stable per install)
// plus a fixed salt. This makes a sealed blob OPAQUE and MACHINE-BOUND (it can't be
// opened on another host). Root can re-derive the key (machine-id is root-readable) —
// the same threat model as DPAPI machine scope, where SYSTEM/admin can also unseal. A
// TPM-sealed key is the planned admin-proof upgrade.

func machineSealKey() ([]byte, error) {
	var id string
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(p); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				id = s
				break
			}
		}
	}
	if id == "" {
		return nil, errors.New("no machine-id available for SealMachine")
	}
	sum := sha256.Sum256([]byte("AegisOne.seal.machine.v1:" + id))
	return sum[:], nil
}

func newMachineGCM() (cipher.AEAD, error) {
	k, err := machineSealKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func SealMachine(data []byte) ([]byte, error) {
	gcm, err := newMachineGCM()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, data, nil), nil // nonce || ciphertext
}

func OpenMachine(blob []byte) ([]byte, error) {
	gcm, err := newMachineGCM()
	if err != nil {
		return nil, err
	}
	if len(blob) < gcm.NonceSize() {
		return nil, errors.New("sealed blob too short")
	}
	nonce, ct := blob[:gcm.NonceSize()], blob[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}
