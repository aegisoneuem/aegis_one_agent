//go:build !windows

package config

// DeleteRegistryValue is a no-op off Windows (there is no registry). registryLookup
// also stays nil, so resolve() simply skips the registry tier.
func DeleteRegistryValue(name string) error { return nil }
