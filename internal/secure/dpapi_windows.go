//go:build windows

package secure

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// Minimal DPAPI (Data Protection API) binding via crypt32.dll — no external
// dependency. We protect the AES master key at machine scope so the agent's
// LocalSystem service can decrypt it without any interactive user context.
var (
	crypt32             = syscall.NewLazyDLL("crypt32.dll")
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procCryptProtect    = crypt32.NewProc("CryptProtectData")
	procCryptUnprotect  = crypt32.NewProc("CryptUnprotectData")
	procLocalFree       = kernel32.NewProc("LocalFree")
)

const (
	cryptProtectUIForbidden  = 0x1 // never show a UI prompt (we run headless)
	cryptProtectLocalMachine = 0x4 // machine scope, not per-user
)

// dataBlob mirrors the Win32 DATA_BLOB { DWORD cbData; BYTE *pbData }.
type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newBlob(d []byte) dataBlob {
	if len(d) == 0 {
		return dataBlob{}
	}
	return dataBlob{cbData: uint32(len(d)), pbData: &d[0]}
}

func (b dataBlob) toBytes() []byte {
	if b.cbData == 0 || b.pbData == nil {
		return nil
	}
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, b.cbData))
	return out
}

// dpapiProtect encrypts data at machine scope, binding it with entropy (an
// app-specific salt) so the blob cannot be trivially unprotected by unrelated
// software on the same machine.
func dpapiProtect(data, entropy []byte) ([]byte, error) {
	in := newBlob(data)
	ent := newBlob(entropy)
	var out dataBlob
	r, _, err := procCryptProtect.Call(
		uintptr(unsafe.Pointer(&in)),
		0, // szDataDescr (unused)
		uintptr(unsafe.Pointer(&ent)),
		0, 0, // reserved, prompt struct
		cryptProtectLocalMachine|cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	// Keep the input buffers alive across the syscall.
	runtime.KeepAlive(data)
	runtime.KeepAlive(entropy)
	if r == 0 {
		return nil, fmt.Errorf("CryptProtectData failed: %w", err)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return out.toBytes(), nil
}

// dpapiUnprotect reverses dpapiProtect using the same entropy.
func dpapiUnprotect(data, entropy []byte) ([]byte, error) {
	in := newBlob(data)
	ent := newBlob(entropy)
	var out dataBlob
	r, _, err := procCryptUnprotect.Call(
		uintptr(unsafe.Pointer(&in)),
		0, // ppszDataDescr (unused)
		uintptr(unsafe.Pointer(&ent)),
		0, 0, // reserved, prompt struct
		cryptProtectLocalMachine|cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	runtime.KeepAlive(data)
	runtime.KeepAlive(entropy)
	if r == 0 {
		return nil, fmt.Errorf("CryptUnprotectData failed: %w", err)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return out.toBytes(), nil
}
