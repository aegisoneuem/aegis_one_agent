//go:build windows

package softdist

import (
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const uninstallRegistryPath = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`

type uninstallRoot struct {
	key        registry.Key
	path, view string
	flags      uint32
}

func machineUninstallRoots() ([]uninstallRoot, error) {
	wide := runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"
	if !wide {
		if err := windows.IsWow64Process(windows.CurrentProcess(), &wide); err != nil {
			return nil, fmt.Errorf("determine registry architecture: %w", err)
		}
	}
	roots := []uninstallRoot{{registry.LOCAL_MACHINE, uninstallRegistryPath, "HKLM32", registry.WOW64_32KEY}}
	if wide {
		roots = append(roots, uninstallRoot{registry.LOCAL_MACHINE, uninstallRegistryPath, "HKLM64", registry.WOW64_64KEY})
	}
	return roots, nil
}

func ResolveInstalledApp(s AppSelector) (*InstalledApp, error) {
	roots, err := machineUninstallRoots()
	if err != nil {
		return nil, err
	}
	if s.UserSID != "" {
		sid, err := windows.StringToSid(s.UserSID)
		if err != nil {
			return nil, fmt.Errorf("invalid execution SID")
		}
		base := sid.String()
		// HKCU Software is largely shared rather than WOW64 redirected. Enumerate
		// the two actual registration paths, not the same shared key twice.
		roots = []uninstallRoot{
			{registry.USERS, base + `\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, "HKCU64", 0},
			{registry.USERS, base + `\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`, "HKCU32", 0},
		}
		profile, err := registry.OpenKey(registry.USERS, base, registry.QUERY_VALUE)
		if err != nil {
			return nil, fmt.Errorf("execution user's registry profile is unavailable: %w", err)
		}
		profile.Close()
	}
	return resolveRegistryApp(roots, s)
}

// Compatibility only. Deploy/verification callers use ResolveInstalledApp so a
// read failure or ambiguous name can never turn into NOT_APPLICABLE/SUCCESS.
func FindInstalledApp(key, name string) *InstalledApp {
	app, _ := ResolveInstalledApp(AppSelector{UninstallKey: key, DisplayName: name})
	return app
}

func resolveRegistryApp(roots []uninstallRoot, s AppSelector) (*InstalledApp, error) {
	key, view, err := s.keyAndView()
	if err != nil {
		return nil, err
	}
	if key == "" && strings.TrimSpace(s.DisplayName) == "" {
		return nil, fmt.Errorf("registry detection needs uninstall_key or display_name")
	}
	var apps []InstalledApp
	for _, root := range roots {
		if view != "" && root.view != view {
			continue
		}
		k, err := registry.OpenKey(root.key, root.path, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE|root.flags)
		if errors.Is(err, registry.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s uninstall registry: %w", root.view, err)
		}
		names := []string{key}
		if key == "" {
			names, err = k.ReadSubKeyNames(-1)
		}
		k.Close()
		if err != nil {
			return nil, fmt.Errorf("enumerate %s uninstall registry: %w", root.view, err)
		}
		for _, name := range names {
			app, err := readInstalledApp(root, name, s)
			if err != nil {
				return nil, err
			}
			if app != nil {
				apps = append(apps, *app)
			}
		}
	}
	return selectInstalledApp(apps, s)
}

func readInstalledApp(root uninstallRoot, subkey string, selector AppSelector) (*InstalledApp, error) {
	k, err := registry.OpenKey(root.key, root.path+`\`+subkey, registry.QUERY_VALUE|root.flags)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	} // removed concurrently
	if err != nil {
		return nil, fmt.Errorf("read %s\\%s: %w", root.view, subkey, err)
	}
	defer k.Close()
	app := &InstalledApp{Key: subkey, RegistryView: root.view}
	// Read the name first so an unrelated vendor's malformed command/version
	// cannot prevent a narrowly targeted operation. Explicit nameless keys work.
	name, _, err := k.GetStringValue("DisplayName")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return nil, fmt.Errorf("read %s\\%s DisplayName: %w", root.view, subkey, err)
	}
	app.DisplayName = name
	wantName := strings.TrimSpace(selector.DisplayName)
	if wantName != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(wantName)) {
		return nil, nil
	}
	if strings.TrimSpace(selector.UninstallKey) == "" && strings.TrimSpace(name) == "" {
		return nil, nil
	}
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"DisplayVersion", &app.DisplayVersion},
		{"Publisher", &app.Publisher}, {"UninstallString", &app.UninstallString},
		{"QuietUninstallString", &app.QuietUninstallString},
		{"InstallDate", &app.InstallDate},
	} {
		value, _, err := k.GetStringValue(field.name)
		if errors.Is(err, registry.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s\\%s %s: %w", root.view, subkey, field.name, err)
		}
		*field.value = value
	}
	value, _, err := k.GetIntegerValue("WindowsInstaller")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return nil, fmt.Errorf("read %s\\%s WindowsInstaller: %w", root.view, subkey, err)
	}
	app.WindowsInstaller = value == 1
	sizeKB, _, err := k.GetIntegerValue("EstimatedSize")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return nil, fmt.Errorf("read %s\\%s EstimatedSize: %w", root.view, subkey, err)
	}
	app.EstimatedSizeKB = int64(sizeKB)
	// A nameless entry can still be selected explicitly by key, but must not
	// accidentally match a display-name search.
	return app, nil
}

// EnumerateInstalledApps returns every registration in the Windows Uninstall
// hives on the device: HKLM native + WOW6432Node, PLUS every LOADED user hive
// under HKEY_USERS (per-user installs — Chrome, Teams, Zoom and similar often
// register under the user profile instead of HKLM). This matches what
// appwiz.cpl and Settings → Apps list for the device.
//
// No limit is imposed on the returned list: however many apps are installed,
// the full inventory is returned. The server catalog (BigFix lists ~350
// apps; the Aegis server team targets 1000+) is the only policy point for
// which apps are patched; the agent only reports facts.
//
// Only LOADED user hives are enumerated. A logged-off user's hive is not
// loaded by the LocalSystem service — profile-load from a service has
// reliability + privacy implications. The common cases (interactive user,
// shared-machine multi-user sessions) are covered as long as the user has
// signed in at least once since boot. BigFix and Tanium behave the same way.
//
// Entries with no DisplayName are skipped — they are typically Windows
// Installer rollback artifacts that are not useful for catalog matching.
func EnumerateInstalledApps() ([]InstalledApp, error) {
	roots, err := machineUninstallRoots()
	if err != nil {
		return nil, err
	}
	userRoots, err := userUninstallRoots()
	if err != nil {
		return nil, err
	}
	roots = append(roots, userRoots...)
	var out []InstalledApp
	for _, root := range roots {
		k, err := registry.OpenKey(root.key, root.path, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE|root.flags)
		if errors.Is(err, registry.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("open %s uninstall registry: %w", root.view, err)
		}
		names, err := k.ReadSubKeyNames(-1)
		k.Close()
		if err != nil {
			return nil, fmt.Errorf("enumerate %s uninstall registry: %w", root.view, err)
		}
		for _, name := range names {
			app, err := readInstalledApp(root, name, AppSelector{})
			if err != nil {
				// Skip malformed/unreadable subkeys instead of failing the whole
				// enumeration: a single corrupt registration must not deny the
				// server its inventory snapshot.
				continue
			}
			if app == nil || strings.TrimSpace(app.DisplayName) == "" {
				continue
			}
			out = append(out, *app)
		}
	}
	return out, nil
}

// userUninstallRoots returns the per-user Uninstall roots for every LOADED
// user hive under HKEY_USERS (native + WOW6432Node mirror). Service accounts
// (S-1-5-18/19/20), the .DEFAULT hive, and the _Classes shadow hives that
// mirror each loaded user hive are skipped. A user whose SOFTWARE path is
// missing is handled silently by the caller's existing ErrNotExist branch.
func userUninstallRoots() ([]uninstallRoot, error) {
	k, err := registry.OpenKey(registry.USERS, "", registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil, fmt.Errorf("open HKEY_USERS: %w", err)
	}
	names, err := k.ReadSubKeyNames(-1)
	k.Close()
	if err != nil {
		return nil, fmt.Errorf("enumerate HKEY_USERS: %w", err)
	}
	var roots []uninstallRoot
	for _, sid := range names {
		if !isLoadedUserSID(sid) {
			continue
		}
		roots = append(roots,
			uninstallRoot{
				key:  registry.USERS,
				path: sid + `\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`,
				view: `HKU\` + sid,
			},
			uninstallRoot{
				key:  registry.USERS,
				path: sid + `\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`,
				view: `HKU\` + sid + `\WOW6432Node`,
			},
		)
	}
	return roots, nil
}

// isLoadedUserSID returns true for a real interactive user SID (S-1-5-21-...)
// and false for service accounts (S-1-5-18/19/20), the .DEFAULT hive, and the
// _Classes shadow hives that mirror each loaded user hive at HKU root.
func isLoadedUserSID(name string) bool {
	if strings.HasSuffix(name, "_Classes") {
		return false
	}
	return strings.HasPrefix(name, "S-1-5-21-")
}
