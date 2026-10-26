//go:build windows

package secure

import (
	"fmt"
	"runtime"
	"unsafe"
)

var userSecretEntropy = []byte("AegisOne/mock-softdist-user-secrets/v1")

// ProtectUserData is for the mock operator's credential fixture only. It uses
// current-user DPAPI, not machine scope. Production servers use their own vault
// and encrypted durable job storage; this is not a cross-machine secret file.
func ProtectUserData(data []byte) ([]byte, error) {
	in, entropy := newBlob(data), newBlob(userSecretEntropy)
	var out dataBlob
	ok, _, err := procCryptProtect.Call(uintptr(unsafe.Pointer(&in)), 0, uintptr(unsafe.Pointer(&entropy)), 0, 0, cryptProtectUIForbidden, uintptr(unsafe.Pointer(&out)))
	runtime.KeepAlive(data)
	if ok == 0 {
		return nil, fmt.Errorf("protect operator secret data: %w", err)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	defer clear(unsafe.Slice(out.pbData, int(out.cbData)))
	return out.toBytes(), nil
}

func UnprotectUserData(data []byte) ([]byte, error) {
	in, entropy := newBlob(data), newBlob(userSecretEntropy)
	var out dataBlob
	ok, _, err := procCryptUnprotect.Call(uintptr(unsafe.Pointer(&in)), 0, uintptr(unsafe.Pointer(&entropy)), 0, 0, cryptProtectUIForbidden, uintptr(unsafe.Pointer(&out)))
	runtime.KeepAlive(data)
	if ok == 0 {
		return nil, fmt.Errorf("unprotect operator secret data: %w", err)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	defer clear(unsafe.Slice(out.pbData, int(out.cbData)))
	return out.toBytes(), nil
}
