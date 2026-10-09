//go:build windows

package config

import "golang.org/x/sys/windows/registry"

const registryPath = `SOFTWARE\AegisOne\Client`

// On Windows the MSI installer writes config values under
// HKLM\SOFTWARE\AegisOne\Client (value name = the env-var key), so the agent is
// configured without a config file. Registry is a lower priority than env/file.
func init() {
	registryLookup = func(key string) string {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, registryPath,
			registry.QUERY_VALUE|registry.WOW64_64KEY)
		if err != nil {
			return ""
		}
		defer k.Close()
		v, _, err := k.GetStringValue(key)
		if err != nil {
			return ""
		}
		return v
	}
}

// DeleteRegistryValue removes a value from HKLM\SOFTWARE\AegisOne\Client. Used to
// erase the enrollment token after a successful enroll (single-use bootstrap).
func DeleteRegistryValue(name string) error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, registryPath,
		registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.DeleteValue(name)
}
