//go:build windows

package secure

import (
	"bytes"
	"testing"
)

func TestOperatorCredentialDPAPI(t *testing.T) {
	plain := []byte("private-operator-canary-28be")
	sealed, err := ProtectUserData(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, plain) {
		t.Fatal("plaintext present in encrypted bundle")
	}
	opened, err := UnprotectUserData(sealed)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(opened)
	if !bytes.Equal(opened, plain) {
		t.Fatal("DPAPI roundtrip mismatch")
	}
	sealed[len(sealed)/2] ^= 0x40
	if data, err := UnprotectUserData(sealed); err == nil {
		clear(data)
		t.Fatal("tampered DPAPI accepted")
	}
}
