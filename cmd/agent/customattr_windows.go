//go:build windows

package main

import (
	"strconv"
	"strings"

	"golang.org/x/sys/windows/registry"
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

// evalFileVersion reads a file's version resource. Deferred (needs version.dll);
// returns unsupported for now so the "version" attr is simply omitted.
func evalFileVersion(path string) (string, bool) {
	return "", false
}
