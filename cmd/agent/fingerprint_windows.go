//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

var (
	fpKernel32            = syscall.NewLazyDLL("kernel32.dll")
	procGetSystemFirmware = fpKernel32.NewProc("GetSystemFirmwareTable")
)

// hardwareIDs returns the raw hardware identifiers (SMBIOS System UUID, registry
// MachineGuid) sent to the server at enrollment for de-duplication. Either may be
// "" if unreadable.
func hardwareIDs() (systemUUID, machineGUID string) {
	return systemUUIDValue(), machineGUIDValue()
}

// hardwareFingerprint returns a stable per-machine fingerprint for clone/reimage
// detection (and, later, server-side dedup). It prefers the SMBIOS System UUID
// (unique per physical/virtual machine, survives OS reinstall, differs across VM
// clones) and falls back to the registry MachineGuid; "" if neither is readable.
func hardwareFingerprint() string {
	if u := systemUUIDValue(); u != "" {
		return "smbios:" + u
	}
	if g := machineGUIDValue(); g != "" {
		return "machineguid:" + g
	}
	return ""
}

func machineGUIDValue() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// systemUUIDValue reads the SMBIOS System Information (Type 1) UUID via
// GetSystemFirmwareTable('RSMB'). Returns "" if unavailable or unset.
func systemUUIDValue() string {
	const rsmb = 0x52534D42 // 'RSMB' provider signature
	size, _, _ := procGetSystemFirmware.Call(uintptr(rsmb), 0, 0, 0)
	if size == 0 {
		return ""
	}
	buf := make([]byte, size)
	n, _, _ := procGetSystemFirmware.Call(uintptr(rsmb), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 || int(n) > len(buf) || n < 8 {
		return ""
	}
	buf = buf[:n]
	// RawSMBIOSData header: [4:8]=Length (DWORD), [8:]=SMBIOS table data.
	tableLen := binary.LittleEndian.Uint32(buf[4:8])
	table := buf[8:]
	if int(tableLen) <= len(table) {
		table = table[:tableLen]
	}
	return parseSMBIOSSystemUUID(table)
}

// parseSMBIOSSystemUUID walks the SMBIOS structure table and returns the UUID
// from the first Type 1 (System Information) structure.
func parseSMBIOSSystemUUID(t []byte) string {
	i := 0
	for i+4 <= len(t) {
		typ := t[i]
		length := int(t[i+1]) // length of the formatted area (incl. 4-byte header)
		if length < 4 || i+length > len(t) {
			break
		}
		if typ == 1 && i+0x08+16 <= len(t) { // System Information: UUID at offset 0x08
			return formatSMBIOSUUID(t[i+0x08 : i+0x08+16])
		}
		// Advance past the formatted area and the string-set (double-NUL terminated).
		j := i + length
		for j+1 < len(t) && !(t[j] == 0 && t[j+1] == 0) {
			j++
		}
		i = j + 2
	}
	return ""
}

func formatSMBIOSUUID(b []byte) string {
	if len(b) != 16 {
		return ""
	}
	allZero, allFF := true, true
	for _, x := range b {
		if x != 0x00 {
			allZero = false
		}
		if x != 0xFF {
			allFF = false
		}
	}
	if allZero || allFF {
		return "" // UUID not set / not present
	}
	// SMBIOS 2.6+: the first three groups are stored little-endian — reverse them
	// to match the canonical (and WMI Win32_ComputerSystemProduct) representation.
	return strings.ToUpper(fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[3], b[2], b[1], b[0],
		b[5], b[4],
		b[7], b[6],
		b[8], b[9],
		b[10], b[11], b[12], b[13], b[14], b[15]))
}
