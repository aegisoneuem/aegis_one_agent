//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	versionDLL                  = windows.NewLazySystemDLL("version.dll")
	procGetFileVersionInfoSizeW = versionDLL.NewProc("GetFileVersionInfoSizeW")
	procGetFileVersionInfoW     = versionDLL.NewProc("GetFileVersionInfoW")
	procVerQueryValueW          = versionDLL.NewProc("VerQueryValueW")
)

// regRoot maps a root name to its predefined HKEY. Read-only roots only.
func regRoot(s string) (registry.Key, bool) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "HKLM", "HKEY_LOCAL_MACHINE":
		return registry.LOCAL_MACHINE, true
	case "HKCU", "HKEY_CURRENT_USER":
		return registry.CURRENT_USER, true
	case "HKCR", "HKEY_CLASSES_ROOT":
		return registry.CLASSES_ROOT, true
	case "HKU", "HKEY_USERS":
		return registry.USERS, true
	default:
		return 0, false
	}
}

// evalRegistryAttr reads one registry value (string or integer) READ-ONLY. An
// empty name reads the key's default value.
func evalRegistryAttr(root, key, name string) (string, bool) {
	rk, ok := regRoot(root)
	if !ok || key == "" {
		return "", false
	}
	k, err := registry.OpenKey(rk, key, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	if s, _, err := k.GetStringValue(name); err == nil {
		return s, true
	}
	if n, _, err := k.GetIntegerValue(name); err == nil {
		return strconv.FormatUint(n, 10), true
	}
	return "", false
}

// evalAdminTag reads an admin-set tag from HKLM\SOFTWARE\AegisOne\Client\Tags
// (e.g. department/site set at deploy time) — a safe, known, read-only location.
func evalAdminTag(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	return evalRegistryAttr("HKLM", `SOFTWARE\AegisOne\Client\Tags`, name)
}

// evalServiceState returns a Windows service's current state (running | stopped |
// not_installed | ...). Least-privilege: SC_MANAGER_CONNECT + SERVICE_QUERY_STATUS
// only (no admin needed to read a service's state), native SCM — no WMI.
func evalServiceState(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "", false
	}
	defer windows.CloseServiceHandle(scm)
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return "", false
	}
	svcH, err := windows.OpenService(scm, namePtr, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return "not_installed", true // a definite answer: the service does not exist
	}
	defer windows.CloseServiceHandle(svcH)
	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(svcH, &st); err != nil {
		return "", false
	}
	return serviceStateString(st.CurrentState), true
}

func serviceStateString(s uint32) string {
	switch s {
	case windows.SERVICE_STOPPED:
		return "stopped"
	case windows.SERVICE_START_PENDING:
		return "start_pending"
	case windows.SERVICE_STOP_PENDING:
		return "stop_pending"
	case windows.SERVICE_RUNNING:
		return "running"
	case windows.SERVICE_CONTINUE_PENDING:
		return "continue_pending"
	case windows.SERVICE_PAUSE_PENDING:
		return "pause_pending"
	case windows.SERVICE_PAUSED:
		return "paused"
	default:
		return "unknown"
	}
}

// evalFileVersion reads a file's VS_FIXEDFILEINFO version (a.b.c.d) via version.dll
// — e.g. the version of an app's exe/dll. Returns false when the file has no
// version resource. Native, no WMI.
func evalFileVersion(path string) (string, bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", false
	}
	var handle uint32
	size, _, _ := procGetFileVersionInfoSizeW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&handle)))
	if size == 0 {
		return "", false
	}
	buf := make([]byte, size)
	if r, _, _ := procGetFileVersionInfoW.Call(uintptr(unsafe.Pointer(p)), 0, size, uintptr(unsafe.Pointer(&buf[0]))); r == 0 {
		return "", false
	}
	sub, _ := windows.UTF16PtrFromString(`\`)
	var fixedPtr uintptr
	var fixedLen uint32
	if r, _, _ := procVerQueryValueW.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(sub)), uintptr(unsafe.Pointer(&fixedPtr)), uintptr(unsafe.Pointer(&fixedLen))); r == 0 || fixedPtr == 0 || fixedLen == 0 {
		return "", false
	}
	// Read FileVersionMS (offset 8) + FileVersionLS (offset 12) by offset WITHIN buf
	// (don't hold a raw uintptr across a possible GC move).
	off := fixedPtr - uintptr(unsafe.Pointer(&buf[0]))
	if off+16 > uintptr(len(buf)) {
		return "", false
	}
	ms := binary.LittleEndian.Uint32(buf[off+8:])
	ls := binary.LittleEndian.Uint32(buf[off+12:])
	return fmt.Sprintf("%d.%d.%d.%d", ms>>16, ms&0xFFFF, ls>>16, ls&0xFFFF), true
}
