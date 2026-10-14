//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"syscall"
	"unsafe"
)

// TPM-backed hardware identity. The TPM Endorsement Key (EK) is burned into the
// TPM at manufacture: globally unique, survives an OS reinstall/format, and cannot
// be cloned — a stronger identity than the SMBIOS UUID (which some VMs/OEM batches
// leave duplicated or all-zero). We read the EK PUBLIC via the Windows TPM Base
// Services (TBS) API and a raw TPM2_ReadPublic command — no PowerShell, no COM.
//
// Everything degrades gracefully: no TPM, TBS unavailable, or any parse hiccup →
// returns "" and the caller falls back to the SMBIOS UUID (then MachineGuid).
var (
	tbsDLL                 = syscall.NewLazyDLL("tbs.dll")
	procTbsiContextCreate  = tbsDLL.NewProc("Tbsi_Context_Create")
	procTbsipSubmitCommand = tbsDLL.NewProc("Tbsip_Submit_Command")
	procTbsipContextClose  = tbsDLL.NewProc("Tbsip_Context_Close")
)

// RSA and ECC EK persistent handles (TCG EK Credential Profile).
var ekHandles = []uint32{0x81010001, 0x81010002}

// tpmEKFingerprint returns a stable, unique fingerprint derived from the TPM EK
// public key, or "" if there is no readable TPM.
func tpmEKFingerprint() string {
	// TBS_CONTEXT_PARAMS2{ version: TBS_CONTEXT_VERSION_TWO(2), flags: includeTpm20(0x4) }
	params := struct{ version, flags uint32 }{2, 0x4}
	var ctx uintptr
	if r, _, _ := procTbsiContextCreate.Call(uintptr(unsafe.Pointer(&params)), uintptr(unsafe.Pointer(&ctx))); r != 0 || ctx == 0 {
		return ""
	}
	defer procTbsipContextClose.Call(ctx)

	for _, h := range ekHandles {
		if pub := readPublic(ctx, h); pub != "" {
			return pub
		}
	}
	return ""
}

// readPublic issues TPM2_ReadPublic for one persistent handle and returns a hash
// of the returned TPMT_PUBLIC (deterministic per EK), prefixed "tpm:". "" on any failure.
func readPublic(ctx uintptr, handle uint32) string {
	// TPM2_ReadPublic command (big-endian):
	//   tag=TPM_ST_NO_SESSIONS(0x8001) | commandSize(14) | TPM_CC_ReadPublic(0x173) | objectHandle
	cmd := make([]byte, 14)
	binary.BigEndian.PutUint16(cmd[0:2], 0x8001)
	binary.BigEndian.PutUint32(cmd[2:6], 14)
	binary.BigEndian.PutUint32(cmd[6:10], 0x00000173)
	binary.BigEndian.PutUint32(cmd[10:14], handle)

	resp := make([]byte, 4096)
	respLen := uint32(len(resp))
	// Tbsip_Submit_Command(ctx, locality=0, priority=TBS_COMMAND_PRIORITY_NORMAL(200), cmd, len, resp, &respLen)
	r, _, _ := procTbsipSubmitCommand.Call(ctx, 0, 200,
		uintptr(unsafe.Pointer(&cmd[0])), uintptr(len(cmd)),
		uintptr(unsafe.Pointer(&resp[0])), uintptr(unsafe.Pointer(&respLen)))
	if r != 0 || respLen < 12 {
		return ""
	}
	resp = resp[:respLen]
	// Response header: tag(2) responseSize(4) responseCode(4). rc!=0 → EK not present at this handle.
	if binary.BigEndian.Uint32(resp[6:10]) != 0 {
		return ""
	}
	// Body: outPublic = TPM2B_PUBLIC{ size(2) + TPMT_PUBLIC[size] }. Hash the TPMT_PUBLIC.
	pubSize := int(binary.BigEndian.Uint16(resp[10:12]))
	if pubSize == 0 || 12+pubSize > len(resp) {
		return ""
	}
	sum := sha256.Sum256(resp[12 : 12+pubSize])
	return "tpm:" + hex.EncodeToString(sum[:])
}
