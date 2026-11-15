//go:build !windows

package main

// Non-Windows stubs. The registry/admin-tag/file-version providers are Windows-
// specific; env + file exists/size work cross-platform via customattr.go. Linux/
// macOS equivalents (e.g. a config/plist provider) land with those agent ports.
func evalRegistryAttr(root, key, name string) (string, bool) { return "", false }
func evalAdminTag(name string) (string, bool)                { return "", false }
func evalFileVersion(path string) (string, bool)             { return "", false }
