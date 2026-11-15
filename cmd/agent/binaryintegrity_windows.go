//go:build windows

package main

import "golang.org/x/sys/windows/registry"

// The binary baseline lives in the agent's own registry tree
// (HKLM\SOFTWARE\AegisOne\Client, values BinaryVersion + BinaryHash) alongside
// the key-provisioned marker and policy floor — one SYSTEM-writable location.
const (
	binBaselineRegPath   = `SOFTWARE\AegisOne\Client`
	binBaselineVerValue  = "BinaryVersion"
	binBaselineHashValue = "BinaryHash"
)

func platformReadBinaryBaseline() (version, hash string) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, binBaselineRegPath, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", ""
	}
	defer k.Close()
	version, _, _ = k.GetStringValue(binBaselineVerValue)
	hash, _, _ = k.GetStringValue(binBaselineHashValue)
	return version, hash
}

func platformWriteBinaryBaseline(version, hash string) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, binBaselineRegPath, registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetStringValue(binBaselineVerValue, version); err != nil {
		return err
	}
	return k.SetStringValue(binBaselineHashValue, hash)
}
