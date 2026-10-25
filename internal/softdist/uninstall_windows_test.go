//go:build windows

package softdist

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func TestRegistryUninstallPlan(t *testing.T) {
	const code = "{23170F69-40C1-2702-2604-000001000000}"
	for _, tc := range []struct {
		name    string
		app     InstalledApp
		p       Params
		bin     string
		args    []string
		wantErr bool
	}{
		{"quiet vendor command", InstalledApp{QuietUninstallString: `"C:\Program Files\Acme\remove.exe" /S /LOG="C:\temp\a b.log"`}, Params{SilentArgs: "/ignored"}, `C:\Program Files\Acme\remove.exe`, []string{"/S", `/LOG=C:\temp\a b.log`}, false},
		{"quoted custom argument", InstalledApp{UninstallString: `"C:\Acme\remove.exe"`}, Params{SilentArgs: `/S /LOG="${folder}\a.log"`, Parameters: map[string]string{"folder": `C:\temp\a b`}}, `C:\Acme\remove.exe`, []string{"/S", `/LOG=C:\temp\a b\a.log`}, false},
		{"MSI maintenance becomes removal", InstalledApp{UninstallString: "MsiExec.exe /I" + code}, Params{}, "msiexec.exe", []string{"/x", code, "/qn", "/norestart", "/l*v", "test.log"}, false},
		{"MSI property without command", InstalledApp{Key: code, WindowsInstaller: true}, Params{}, "msiexec.exe", []string{"/x", code, "/qn", "/norestart", "/l*v", "test.log"}, false},
		{"unquoted spaced executable", InstalledApp{QuietUninstallString: `C:\Program Files\Acme\remove.exe /S`}, Params{}, "", nil, true},
		{"relative vendor binary", InstalledApp{QuietUninstallString: `remove.exe /S`}, Params{}, "", nil, true},
		{"no command", InstalledApp{}, Params{}, "", nil, true},
		{"no silent contract", InstalledApp{UninstallString: `C:\Acme\remove.exe`}, Params{}, "", nil, true},
		{"MSI malformed code", InstalledApp{UninstallString: `msiexec.exe /Ibogus`}, Params{}, "", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, args, err := registryUninstallInvocation(tc.p, &tc.app, "test.log")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("unexpected plan %s %v", bin, args)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if bin != tc.bin || !reflect.DeepEqual(args, tc.args) {
				t.Fatalf("got %q %#v want %q %#v", bin, args, tc.bin, tc.args)
			}
		})
	}
}

func TestWindowsArgumentParsingAndNativeExecution(t *testing.T) {
	if os.Getenv("AEGIS_UNINSTALL_TEST_HELPER") == "1" {
		args := os.Args
		if args[len(args)-1] != `/LOG=C:\temp\with space\out.log` {
			os.Exit(19)
		}
		os.Exit(0)
	}
	args, err := splitWindowsArgs(`-test.run=TestWindowsArgumentParsingAndNativeExecution -- /LOG="C:\temp\with space\out.log"`)
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEGIS_UNINSTALL_TEST_HELPER", "1")
	r := execInstaller(context.Background(), Params{Action: "uninstall", Timeout: 10 * time.Second}, bin, args, "")
	if !r.Success || r.ExitCode != 0 {
		t.Fatalf("native quoted argument lost: %+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = execInstaller(ctx, Params{Action: "uninstall"}, bin, args, "")
	if r.Success || !strings.Contains(r.Reason, "cancelled") {
		t.Fatalf("cancel result %+v", r)
	}
}

func TestRegistryResolverFixtures(t *testing.T) {
	guid, err := windows.GenerateGUID()
	if err != nil {
		t.Fatal(err)
	}
	path := `Software\AegisOneTests\` + guid.String()
	root, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close(); registry.DeleteKey(registry.CURRENT_USER, path) })
	for _, key := range []string{"v1", "v2"} {
		k, _, err := registry.CreateKey(root, key, registry.ALL_ACCESS)
		if err != nil {
			t.Fatal(err)
		}
		if err = k.SetStringValue("DisplayName", "Aegis Fixture Editor"); err != nil {
			t.Fatal(err)
		}
		if err = k.SetStringValue("DisplayVersion", strings.TrimPrefix(key, "v")+".0"); err != nil {
			t.Fatal(err)
		}
		if err = k.SetExpandStringValue("QuietUninstallString", `"%SystemRoot%\System32\cmd.exe" /c exit 0`); err != nil {
			t.Fatal(err)
		}
		k.Close()
		subkey := key
		t.Cleanup(func() { registry.DeleteKey(root, subkey) })
	}
	roots := []uninstallRoot{{registry.CURRENT_USER, path, "HKLM64", 0}}
	if _, err := resolveRegistryApp(roots, AppSelector{DisplayName: "Aegis Fixture"}); err == nil {
		t.Fatal("ambiguous fixture accepted")
	}
	app, err := resolveRegistryApp(roots, AppSelector{DisplayName: "Aegis Fixture", Version: "1.0"})
	if err != nil || app == nil || app.Key != "v1" {
		t.Fatalf("match=%+v err=%v", app, err)
	}
	bin, _, err := registryUninstallInvocation(Params{}, app, "")
	if err != nil || !filepath.IsAbs(bin) || strings.Contains(bin, "%SystemRoot%") {
		t.Fatalf("environment expansion %q %v", bin, err)
	}
	bad, err := registry.OpenKey(root, "v1", registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	bad.SetDWordValue("QuietUninstallString", 1)
	bad.Close()
	if _, err := resolveRegistryApp(roots, AppSelector{UninstallKey: "v1"}); err == nil {
		t.Fatal("bad registry value treated as absent")
	}
	if _, err := resolveRegistryApp([]uninstallRoot{{registry.Key(0), path, "HKLM64", 0}}, AppSelector{DisplayName: "Acme"}); err == nil {
		t.Fatal("registry read failure treated as absent")
	}
}

func TestMSIInvalidCodeIsFailure(t *testing.T) {
	if _, err := QueryProductInstalled("not-a-guid"); err == nil {
		t.Fatal("invalid code treated as absent")
	}
	r := Run(context.Background(), Params{Action: "uninstall", ProductCode: "not-a-guid"})
	if r.Success || r.NotApplicable || !strings.Contains(r.Reason, "invalid MSI") {
		t.Fatalf("result %+v", r)
	}
	if r := classify(Params{Action: "uninstall"}, 1638, ""); r.Success || r.NotApplicable {
		t.Fatalf("uninstall 1638 must fail: %+v", r)
	}
}

func TestCustomUninstallCommandDoesNotFallBackToRegistry(t *testing.T) {
	r := Run(context.Background(), Params{Action: "uninstall", InstallerType: "command", UninstallKey: "Acme"})
	if r.Success || r.NotApplicable || !strings.Contains(r.Reason, "requires file_path") {
		t.Fatalf("missing custom command fell back to registry: %+v", r)
	}
}

func TestMSIUninstallArgumentsCannotChangeAction(t *testing.T) {
	for _, args := range []string{`/qn /i "C:\other.msi"`, `/X{23170F69-40C1-2702-2604-000001000000}`, `/repair`, "/update patch.msp"} {
		if err := validateMSIUninstallArgs(args); err == nil {
			t.Errorf("allowed a second action: %q", args)
		}
	}
	for _, args := range []string{`/qn /norestart REMOVE=ALL`, `/passive /forcerestart`, `/quiet /log "C:\temp\a b.log"`} {
		if err := validateMSIUninstallArgs(args); err != nil {
			t.Errorf("rejected valid options %q: %v", args, err)
		}
	}
	_, args := msiUninstallByCode(Params{SilentArgs: `/qn LOGPATH="${logdir}\u.log"`, Parameters: map[string]string{"logdir": `C:\temp\a b`}}, "{23170F69-40C1-2702-2604-000001000000}", "")
	if len(args) != 4 || args[3] != `LOGPATH=C:\temp\a b\u.log` {
		t.Fatalf("MSI property grouping lost: %#v", args)
	}
}
