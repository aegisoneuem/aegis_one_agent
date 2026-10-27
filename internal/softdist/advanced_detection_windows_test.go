//go:build windows

package softdist

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func TestNativeAdvancedFileVersionAndPresence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.exe")
	if err := os.WriteFile(path, []byte("unversioned file"), 0600); err != nil {
		t.Fatal(err)
	}
	d := Detection{Rules: []DetectionRule{{Kind: "file_version", Path: path, MinimumVersion: "1.0"}}}
	got, err := EvaluateDetection(context.Background(), d, "", false)
	if err != nil || !got.Present || got.Satisfied {
		t.Fatal(got, err)
	}
	got, err = EvaluateDetection(context.Background(), d, "", true)
	if err != nil || !got.Present {
		t.Fatal("unversioned file incorrectly absent for uninstall", got, err)
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	d.Rules[0].Path = filepath.Join(system, "cmd.exe")
	got, err = EvaluateDetection(context.Background(), d, "", false)
	if err != nil || !got.Satisfied {
		t.Fatal("native fixed file version not read", got, err)
	}
	d.Rules[0].MinimumVersion = "65535.65535.65535.65535"
	got, err = EvaluateDetection(context.Background(), d, "", false)
	if err != nil || got.Satisfied || !got.Present {
		t.Fatal("old version falsely satisfies minimum", got, err)
	}
}

func TestNativeSelectedUserRegistryRules(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	path := `Software\AegisOne\DetectionTests\` + filepath.Base(filepath.Dir(t.TempDir()))
	key, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.ALL_ACCESS|registry.WOW64_64KEY)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.DeleteKey(registry.CURRENT_USER, path)
	defer key.Close()
	if err = key.SetStringValue("Region", "apac"); err != nil {
		t.Fatal(err)
	}
	if err = key.SetDWordValue("Count", 42); err != nil {
		t.Fatal(err)
	}
	d := Detection{Mode: "all", Rules: []DetectionRule{{Kind: "registry_value", Hive: "HKCU", Key: path, View: "64", ValueName: "Region", ValueType: "string", ExpectedValue: "apac"}, {Kind: "registry_value", Hive: "HKCU", Key: path, View: "64", ValueName: "Count", ValueType: "dword", ExpectedValue: "42"}}}
	got, err := EvaluateDetection(context.Background(), d, user.User.Sid.String(), false)
	if err != nil || !got.Satisfied {
		t.Fatal(got, err)
	}
	d.Rules[1].ExpectedValue = "43"
	got, err = EvaluateDetection(context.Background(), d, user.User.Sid.String(), false)
	if err != nil || got.Satisfied || !got.Present {
		t.Fatal(got, err)
	}
	if _, err = EvaluateDetection(context.Background(), d, "", false); err == nil {
		t.Fatal("HKCU silently used the service account")
	}
	if _, err = EvaluateDetection(context.Background(), d, "S-1-5-21-111-222-333-444", true); err == nil || !strings.Contains(err.Error(), "profile") {
		t.Fatal("unloaded profile treated as absence", err)
	}
	got, err = EvaluateDetection(context.Background(), d, user.User.Sid.String(), true)
	if err != nil || !got.Present {
		t.Fatal("wrong value treated as removed", got, err)
	}
}
