//go:build windows

package config

import "golang.org/x/sys/windows/registry"

const registryRoot = `SOFTWARE\AegisOne\Client`

// registrySubkeys are the organized sections under the root (BigFix-style).
// Values are stored by their env-var name inside the relevant section; the agent
// reads by searching these sections, so the hierarchy is cosmetic/organizational
// and needs no key→section mapping. The root itself is searched last (fallback).
var registrySubkeys = []string{
	registryRoot + `\Connection`,
	registryRoot + `\Enrollment`,
	registryRoot + `\TLS`,
	registryRoot + `\Paths`,
	registryRoot + `\Settings`,
	registryRoot,
}

// On Windows the MSI installer writes config values under organized subkeys of
// HKLM\SOFTWARE\AegisOne\Client, so the agent is configured without a config file.
// All values are read once into a map at startup. Registry is lower priority than
// env/file (see resolve()).
func init() {
	m := map[string]string{}
	for _, path := range registrySubkeys {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE|registry.WOW64_64KEY)
		if err != nil {
			continue
		}
		names, _ := k.ReadValueNames(0)
		for _, n := range names {
			if _, exists := m[n]; exists {
				continue // earlier section wins over the root fallback
			}
			if v, _, err := k.GetStringValue(n); err == nil {
				m[n] = v
			}
		}
		k.Close()
	}
	registryLookup = func(key string) string { return m[key] }
}

// DeleteRegistryValue removes a value from wherever it lives under the AegisOne
// registry tree (searches the sections). Used to erase the enrollment token after
// a successful enroll (single-use bootstrap).
func DeleteRegistryValue(name string) error {
	var lastErr error
	found := false
	for _, path := range registrySubkeys {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.SET_VALUE|registry.WOW64_64KEY)
		if err != nil {
			continue
		}
		if err := k.DeleteValue(name); err == nil {
			found = true
		} else {
			lastErr = err
		}
		k.Close()
	}
	if found {
		return nil
	}
	return lastErr
}
