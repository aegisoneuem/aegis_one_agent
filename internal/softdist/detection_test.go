package softdist

import (
	"strings"
	"testing"
)

func TestSelectInstalledApp(t *testing.T) {
	apps := []InstalledApp{
		{Key: "Editor-1", RegistryView: "HKLM64", DisplayName: "Acme Editor", DisplayVersion: "1.0"},
		{Key: "Editor-2", RegistryView: "HKLM64", DisplayName: "Acme Editor", DisplayVersion: "2.0"},
		{Key: "Editor-2", RegistryView: "HKLM32", DisplayName: "Acme Editor", DisplayVersion: "2.0"},
		{Key: "Tools", RegistryView: "HKLM64", DisplayName: "Acme Editor Tools", DisplayVersion: "1.0"},
	}
	for _, tc := range []struct {
		name      string
		s         AppSelector
		key, view string
		ambiguous bool
	}{
		{"multiple versions", AppSelector{DisplayName: "Acme Editor"}, "", "", true},
		{"multiple views", AppSelector{UninstallKey: "Editor-2"}, "", "", true},
		{"exact version", AppSelector{DisplayName: "acme editor", Version: "1.0"}, "Editor-1", "HKLM64", false},
		{"view prefix", AppSelector{UninstallKey: `HKLM32\Editor-2`}, "Editor-2", "HKLM32", false},
		{"key and name must agree", AppSelector{UninstallKey: "Editor-1", DisplayName: "Tools"}, "", "", false},
		{"absent version", AppSelector{DisplayName: "Acme Editor", Version: "3.0"}, "", "", false},
		{"unique substring", AppSelector{DisplayName: "Tools"}, "Tools", "HKLM64", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, err := selectInstalledApp(apps, tc.s)
			if tc.ambiguous {
				if err == nil || !strings.Contains(err.Error(), "ambiguous") {
					t.Fatalf("app=%+v err=%v", app, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.key == "" {
				if app != nil {
					t.Fatalf("unexpected match %+v", app)
				}
				return
			}
			if app == nil || app.Key != tc.key || app.RegistryView != tc.view {
				t.Fatalf("wrong match %+v", app)
			}
		})
	}
	app, err := selectInstalledApp(apps[2:], AppSelector{DisplayName: "Acme Editor"})
	if err != nil || app == nil || app.Key != "Editor-2" {
		t.Fatalf("exact name should outrank Tools: %+v %v", app, err)
	}
}

func TestSelectorRejectsArbitraryRegistryPaths(t *testing.T) {
	for _, key := range []string{`HKCU\Editor`, `HKU\S-1-5-21\Editor`, `HKLM64\`, `..\Editor`, "bad\x00key"} {
		if _, _, err := (AppSelector{UninstallKey: key}).keyAndView(); err == nil {
			t.Errorf("accepted %q", key)
		}
	}
}
