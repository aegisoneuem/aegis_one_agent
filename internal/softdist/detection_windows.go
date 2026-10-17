//go:build windows

package softdist

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	modmsi                   = windows.NewLazySystemDLL("msi.dll")
	procMsiQueryProductState = modmsi.NewProc("MsiQueryProductStateW")
)

// INSTALLSTATE values (msi.h). A product query returns DEFAULT when installed and
// usable; LOCAL/SOURCE are feature-level "present" states; ADVERTISED/ABSENT/
// UNKNOWN mean not installed.
const (
	installStateLocal   = 3
	installStateSource  = 4
	installStateDefault = 5
)

// ProductInstalled reports whether the MSI product with the given ProductCode GUID
// (e.g. "{23170F69-40C1-2702-2409-000001000000}") is installed on this machine. An
// empty or malformed code returns false (cannot confirm → act as if not installed).
func ProductInstalled(productCode string) bool {
	productCode = strings.TrimSpace(productCode)
	if productCode == "" {
		return false
	}
	p, err := windows.UTF16PtrFromString(productCode)
	if err != nil {
		return false
	}
	r, _, _ := procMsiQueryProductState.Call(uintptr(unsafe.Pointer(p)))
	switch int32(r) {
	case installStateDefault, installStateLocal, installStateSource:
		return true
	default:
		return false
	}
}

// InstalledApp is what the registry Uninstall hive records about a non-MSI
// (EXE/script-installed) product. UninstallString is the vendor-registered removal
// command; QuietUninstallString, when present, is the vendor's SILENT removal
// command (preferred — no UI).
type InstalledApp struct {
	Key                  string // registry subkey name under the Uninstall hive
	DisplayName          string
	DisplayVersion       string
	Publisher            string
	UninstallString      string
	QuietUninstallString string
}

// uninstallRoots are the machine-wide (HKLM) Uninstall hives: the native view and
// the 32-bit (WOW6432Node) view. Per-user (HKCU) is out of scope — the agent runs
// as LocalSystem and uninstalls machine-wide apps only (per-user = backlog).
var uninstallRoots = []struct {
	root registry.Key
	path string
}{
	{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`},
	{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`},
}

// FindInstalledApp locates a machine-wide installed product in the registry Uninstall
// hive. An exact subkey name (uninstallKey) is matched first; otherwise the product
// is matched by a case-insensitive substring of its DisplayName. Returns nil when no
// match is found (product not installed). Both views (native + WOW6432Node) are
// searched so a 32-bit app on 64-bit Windows is still found.
func FindInstalledApp(uninstallKey, displayName string) *InstalledApp {
	uninstallKey = strings.TrimSpace(uninstallKey)
	displayName = strings.TrimSpace(displayName)
	if uninstallKey == "" && displayName == "" {
		return nil
	}

	for _, r := range uninstallRoots {
		// Exact subkey: open it directly — no enumeration needed.
		if uninstallKey != "" {
			if app := readUninstallSubkey(r.root, r.path, uninstallKey); app != nil {
				return app
			}
			continue
		}
		// DisplayName match: enumerate subkeys and compare.
		if app := findByDisplayName(r.root, r.path, displayName); app != nil {
			return app
		}
	}
	return nil
}

func findByDisplayName(root registry.Key, path, displayName string) *InstalledApp {
	k, err := registry.OpenKey(root, path, registry.ENUMERATE_SUB_KEYS|registry.READ)
	if err != nil {
		return nil
	}
	defer k.Close()
	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil
	}
	want := strings.ToLower(displayName)
	for _, name := range names {
		app := readUninstallSubkey(root, path, name)
		if app != nil && strings.Contains(strings.ToLower(app.DisplayName), want) {
			return app
		}
	}
	return nil
}

// readUninstallSubkey reads one Uninstall subkey into an InstalledApp. A subkey with
// no DisplayName is treated as not-a-product (nil) — those are update/patch entries.
func readUninstallSubkey(root registry.Key, path, subkey string) *InstalledApp {
	k, err := registry.OpenKey(root, path+`\`+subkey, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	name, _, err := k.GetStringValue("DisplayName")
	if err != nil || strings.TrimSpace(name) == "" {
		return nil
	}
	app := &InstalledApp{Key: subkey, DisplayName: name}
	app.DisplayVersion, _, _ = k.GetStringValue("DisplayVersion")
	app.Publisher, _, _ = k.GetStringValue("Publisher")
	app.UninstallString, _, _ = k.GetStringValue("UninstallString")
	app.QuietUninstallString, _, _ = k.GetStringValue("QuietUninstallString")
	return app
}
