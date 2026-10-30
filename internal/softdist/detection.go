package softdist

import (
	"fmt"
	"sort"
	"strings"
)

// InstalledApp identifies one registration, including its registry view.
// InstallDate and EstimatedSizeKB are read for the inventory report path but
// are optional on the detect/verify path (vendors do not reliably populate
// them, so the operation code paths never depend on their presence).
type InstalledApp struct {
	Key, RegistryView, DisplayName, DisplayVersion, Publisher string
	UninstallString, QuietUninstallString                     string
	WindowsInstaller                                          bool
	InstallDate                                               string // yyyymmdd; empty when the vendor did not set it
	EstimatedSizeKB                                           int64  // Uninstall hive EstimatedSize (KB); 0 when missing
}

// AppSelector identifies one registration in the selected scope. Version is an optional
// exact DisplayVersion filter for uninstall, not a minimum-version comparison.
// UninstallKey may have an HKLM32/64 or HKCU32/64 prefix to disambiguate views.
type AppSelector struct {
	UninstallKey, DisplayName, Version string
	UserSID                            string // derived from execution token, never a browser-supplied SID
}

func (s AppSelector) keyAndView() (key, view string, err error) {
	key = strings.TrimSpace(s.UninstallKey)
	for _, prefix := range []string{`HKLM32\`, `HKLM64\`, `HKCU32\`, `HKCU64\`} {
		if strings.HasPrefix(strings.ToUpper(key), prefix) {
			view, key = prefix[:len(prefix)-1], key[len(prefix):]
			break
		}
	}
	if strings.HasPrefix(view, "HKCU") && s.UserSID == "" {
		return "", "", fmt.Errorf("per-user/HKCU removal requires a user execution context")
	}
	if strings.HasPrefix(view, "HKLM") && s.UserSID != "" {
		return "", "", fmt.Errorf("HKLM selector conflicts with user detection scope")
	}
	if strings.ContainsAny(key, `\/:`) || strings.ContainsRune(key, 0) || (view != "" && key == "") {
		return "", "", fmt.Errorf("uninstall_key must be one subkey, optionally prefixed HKLM32\\ or HKLM64\\; per-user/HKCU removal requires user-context support")
	}
	return key, view, nil
}

// Exact DisplayName takes precedence over substring matches. Multiple matches
// fail rather than choosing whichever registration happened to enumerate first.
func selectInstalledApp(apps []InstalledApp, s AppSelector) (*InstalledApp, error) {
	key, view, err := s.keyAndView()
	if err != nil {
		return nil, err
	}
	name, version := strings.TrimSpace(s.DisplayName), strings.TrimSpace(s.Version)
	var matches, exact []InstalledApp
	for _, app := range apps {
		if view != "" && app.RegistryView != view || key != "" && !strings.EqualFold(key, app.Key) {
			continue
		}
		if name != "" && !strings.Contains(strings.ToLower(app.DisplayName), strings.ToLower(name)) {
			continue
		}
		if version != "" && !strings.EqualFold(strings.TrimSpace(app.DisplayVersion), version) {
			continue
		}
		matches = append(matches, app)
		if name != "" && strings.EqualFold(strings.TrimSpace(app.DisplayName), name) {
			exact = append(exact, app)
		}
	}
	if len(exact) > 0 {
		matches = exact
	}
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return &matches[0], nil
	default:
		labels := make([]string, 0, len(matches))
		for _, app := range matches {
			labels = append(labels, fmt.Sprintf("%s\\%s (%s, version %s)", app.RegistryView, app.Key, app.DisplayName, app.DisplayVersion))
		}
		sort.Strings(labels)
		if len(labels) > 8 {
			labels = append(labels[:8], "...")
		}
		return nil, fmt.Errorf("ambiguous installed product (%d matches): %s; specify uninstall_key/view and optionally an exact version", len(matches), strings.Join(labels, "; "))
	}
}
