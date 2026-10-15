//go:build windows

package softdist

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modwintrust        = windows.NewLazySystemDLL("wintrust.dll")
	procWinVerifyTrust = modwintrust.NewProc("WinVerifyTrust")
)

// WINTRUST_ACTION_GENERIC_VERIFY_V2 — the standard Authenticode policy provider.
var wintrustActionGenericVerifyV2 = windows.GUID{
	Data1: 0x00AAC56B, Data2: 0xCD44, Data3: 0x11D0,
	Data4: [8]byte{0x8C, 0xC2, 0x00, 0xC0, 0x4F, 0xC2, 0x95, 0xEE},
}

const (
	wtdUINone              = 2
	wtdRevokeNone          = 0
	wtdChoiceFile          = 1
	wtdStateActionVerify   = 1
	wtdStateActionClose    = 2
	wtdRevocationCheckNone = 0x00000010
)

type wintrustFileInfo struct {
	cbStruct       uint32
	pcwszFilePath  *uint16
	hFile          windows.Handle
	pgKnownSubject *windows.GUID
}

type wintrustData struct {
	cbStruct            uint32
	pPolicyCallbackData uintptr
	pSIPClientData      uintptr
	dwUIChoice          uint32
	fdwRevocationChecks uint32
	dwUnionChoice       uint32
	pFile               uintptr // union member -> *wintrustFileInfo
	dwStateAction       uint32
	hWVTStateData       windows.Handle
	pwszURLReference    *uint16
	dwProvFlags         uint32
	dwUIContext         uint32
	pSignatureSettings  uintptr
}

// verifyAuthenticode returns nil if path carries a valid Authenticode signature
// whose certificate chain is trusted by this machine. It calls WinVerifyTrust
// with the generic verify policy. Revocation is NOT network-checked (the agent is
// offline by design — reaching a CRL/OCSP endpoint would hang or fail on a locked-
// down endpoint), so this proves "signed by a trusted publisher, digest intact,"
// not "certificate not revoked." Returns a descriptive error otherwise.
func verifyAuthenticode(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	fileInfo := wintrustFileInfo{pcwszFilePath: p}
	fileInfo.cbStruct = uint32(unsafe.Sizeof(fileInfo))

	data := wintrustData{
		dwUIChoice:          wtdUINone,
		fdwRevocationChecks: wtdRevokeNone,
		dwUnionChoice:       wtdChoiceFile,
		dwStateAction:       wtdStateActionVerify,
		dwProvFlags:         wtdRevocationCheckNone,
		pFile:               uintptr(unsafe.Pointer(&fileInfo)),
	}
	data.cbStruct = uint32(unsafe.Sizeof(data))

	rc, _, _ := procWinVerifyTrust.Call(0,
		uintptr(unsafe.Pointer(&wintrustActionGenericVerifyV2)),
		uintptr(unsafe.Pointer(&data)))

	// Always release the provider state, whatever the verify result was.
	data.dwStateAction = wtdStateActionClose
	procWinVerifyTrust.Call(0,
		uintptr(unsafe.Pointer(&wintrustActionGenericVerifyV2)),
		uintptr(unsafe.Pointer(&data)))

	if rc == 0 {
		return nil // ERROR_SUCCESS -> trusted
	}
	return fmt.Errorf("%s", authenticodeReason(uint32(rc)))
}

// authenticodeReason maps the common WinVerifyTrust HRESULTs to a concise reason.
func authenticodeReason(code uint32) string {
	switch code {
	case 0x800B0100:
		return "file is not signed (TRUST_E_NOSIGNATURE)"
	case 0x800B0109:
		return "signed, but the root certificate is not trusted (CERT_E_UNTRUSTEDROOT)"
	case 0x800B0111:
		return "the signing certificate is explicitly distrusted (CERT_E_DISTRUST)"
	case 0x80096010:
		return "the file digest does not match the signature (tampered)"
	case 0x800B010C:
		return "the signing certificate was revoked"
	case 0x800B0101:
		return "the signing certificate has expired"
	default:
		return fmt.Sprintf("signature not trusted (0x%08X)", code)
	}
}
