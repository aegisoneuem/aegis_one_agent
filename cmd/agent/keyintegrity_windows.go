//go:build windows

package main

import "golang.org/x/sys/windows/registry"

// The key-provisioned marker lives in the agent's own registry tree
// (HKLM\SOFTWARE\AegisOne\Client, value KeyProvisioned), alongside the policy
// floor — SYSTEM-writable only, one location, and NOT a file next to dpapi.key
// where it could be deleted in the same sweep. It is a flag (1), never the key.
const (
	keyMarkerRegPath  = `SOFTWARE\AegisOne\Client`
	keyMarkerRegValue = "KeyProvisioned"
)

func platformReadKeyMarker(string) bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, keyMarkerRegPath, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(keyMarkerRegValue)
	return err == nil && v == 1
}

func platformWriteKeyMarker(string) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, keyMarkerRegPath, registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetDWordValue(keyMarkerRegValue, 1)
}
