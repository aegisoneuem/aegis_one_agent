//go:build windows

package softdist

import (
	"strings"
	"testing"
)

func TestBuildInvocation(t *testing.T) {
	cases := []struct {
		name     string
		p        Params
		abs      string
		wantBin  string
		wantArgs string // joined for easy compare
	}{
		{"msi install default silent", Params{InstallerType: "msi"}, `C:\p\app.msi`,
			"msiexec.exe", `/i C:\p\app.msi /qn /norestart`},
		{"msi uninstall", Params{InstallerType: "msi", Action: "uninstall"}, `C:\p\app.msi`,
			"msiexec.exe", `/x C:\p\app.msi /qn /norestart`},
		{"msi custom args override default", Params{InstallerType: "msi", SilentArgs: "/quiet"}, `C:\p\app.msi`,
			"msiexec.exe", `/i C:\p\app.msi /quiet`},
		{"auto infers msi", Params{InstallerType: "auto"}, `C:\p\app.msi`,
			"msiexec.exe", `/i C:\p\app.msi /qn /norestart`},
		{"auto infers exe", Params{}, `C:\p\setup.exe`,
			`C:\p\setup.exe`, ``},
		{"exe with silent flag", Params{InstallerType: "exe", SilentArgs: "/S"}, `C:\p\setup.exe`,
			`C:\p\setup.exe`, `/S`},
		{"ps1 payload", Params{InstallerType: "ps1"}, `C:\p\run.ps1`,
			"powershell.exe", `-NonInteractive -NoProfile -ExecutionPolicy Bypass -File C:\p\run.ps1`},
		{"bat payload", Params{InstallerType: "bat"}, `C:\p\run.bat`,
			"cmd.exe", `/c C:\p\run.bat`},
		{"inf driver install", Params{InstallerType: "inf"}, `C:\p\net.inf`,
			"pnputil.exe", `/add-driver C:\p\net.inf /install`},
		{"auto infers inf", Params{}, `C:\p\net.inf`,
			"pnputil.exe", `/add-driver C:\p\net.inf /install`},
		{"msix install default skiplicense", Params{InstallerType: "msix"}, `C:\p\app.msix`,
			"dism.exe", `/online /add-provisionedappxpackage /packagepath:C:\p\app.msix /skiplicense`},
		{"auto infers msix", Params{}, `C:\p\app.msix`,
			"dism.exe", `/online /add-provisionedappxpackage /packagepath:C:\p\app.msix /skiplicense`},
		{"msixbundle with deps (no default skiplicense)", Params{InstallerType: "msixbundle", SilentArgs: "/dependencypackagepath:C:\\p\\vclibs.appx"}, `C:\p\app.msixbundle`,
			"dism.exe", `/online /add-provisionedappxpackage /packagepath:C:\p\app.msixbundle /dependencypackagepath:C:\p\vclibs.appx`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bin, args, err := buildInvocation(c.p, c.abs, "")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if bin != c.wantBin {
				t.Errorf("bin = %q, want %q", bin, c.wantBin)
			}
			if got := strings.Join(args, " "); got != c.wantArgs {
				t.Errorf("args = %q, want %q", got, c.wantArgs)
			}
		})
	}
}

func TestBuildInvocationUnknownType(t *testing.T) {
	if _, _, err := buildInvocation(Params{InstallerType: "rpm"}, `C:\p\x`, ""); err == nil {
		t.Fatal("expected an error for an unknown installer_type")
	}
}

// inf uninstall is intentionally unsupported (needs the published oemNN.inf) —
// it must return a clear error, not silently run pnputil with the wrong name.
func TestBuildInvocationInfUninstallRejected(t *testing.T) {
	if _, _, err := buildInvocation(Params{InstallerType: "inf", Action: "uninstall"}, `C:\p\net.inf`, ""); err == nil {
		t.Fatal("expected an error for inf uninstall via software_deploy")
	}
}

func TestMsixUninstallName(t *testing.T) {
	// Fileless msix uninstall by PackageFullName (via product_code).
	name, ok := msixUninstallName(Params{InstallerType: "msix", Action: "uninstall",
		ProductCode: "Contoso.App_1.0.0.0_x64__abc123"})
	if !ok || name != "Contoso.App_1.0.0.0_x64__abc123" {
		t.Errorf("got (%q,%v), want the package full name + true", name, ok)
	}
	// uninstall_key fallback when product_code empty.
	if n, ok := msixUninstallName(Params{InstallerType: "appx", Action: "uninstall", UninstallKey: "Fam_pub"}); !ok || n != "Fam_pub" {
		t.Errorf("uninstall_key fallback failed: (%q,%v)", n, ok)
	}
	// Not msix / has a file / install action / no name -> not the fileless path.
	for _, p := range []Params{
		{InstallerType: "msi", Action: "uninstall", ProductCode: "x"},               // wrong type
		{InstallerType: "msix", Action: "install", ProductCode: "x"},                // install
		{InstallerType: "msix", Action: "uninstall", FilePath: `C:\a.msix`, ProductCode: "x"}, // has file
		{InstallerType: "msix", Action: "uninstall"},                                // no name
	} {
		if _, ok := msixUninstallName(p); ok {
			t.Errorf("should NOT be fileless msix uninstall: %+v", p)
		}
	}
}

func TestMsixRemoveByName(t *testing.T) {
	bin, args := msixRemoveByName("Contoso.App_1.0.0.0_x64__abc123", "")
	if bin != "dism.exe" {
		t.Errorf("bin = %q", bin)
	}
	want := "/online /remove-provisionedappxpackage /packagename:Contoso.App_1.0.0.0_x64__abc123"
	if got := strings.Join(args, " "); got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
}

// msix uninstall via buildInvocation (file path present, no name) is rejected
// with a clear error — removal must go through the fileless by-name path.
func TestBuildInvocationMsixUninstallRejected(t *testing.T) {
	if _, _, err := buildInvocation(Params{InstallerType: "msix", Action: "uninstall"}, `C:\p\app.msix`, ""); err == nil {
		t.Fatal("expected an error for file-based msix uninstall via buildInvocation")
	}
}

func TestIsFilelessMSIUninstall(t *testing.T) {
	code := "{23170F69-40C1-2702-2604-000001000000}"
	cases := []struct {
		name string
		p    Params
		want bool
	}{
		{"uninstall by code, no file, msi", Params{Action: "uninstall", InstallerType: "msi", ProductCode: code}, true},
		{"uninstall by code, no file, auto", Params{Action: "uninstall", InstallerType: "auto", ProductCode: code}, true},
		{"uninstall by code, no file, empty type", Params{Action: "uninstall", ProductCode: code}, true},
		{"uninstall with a file present → file-based", Params{Action: "uninstall", ProductCode: code, FilePath: `C:\p\app.msi`}, false},
		{"uninstall without a code", Params{Action: "uninstall", InstallerType: "msi"}, false},
		{"install is never fileless", Params{Action: "install", InstallerType: "msi", ProductCode: code}, false},
		{"exe uninstall is not MSI", Params{Action: "uninstall", InstallerType: "exe", ProductCode: code}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isFilelessMSIUninstall(c.p); got != c.want {
				t.Errorf("isFilelessMSIUninstall = %v, want %v", got, c.want)
			}
		})
	}
}

func TestMSIUninstallByCode(t *testing.T) {
	code := "{23170F69-40C1-2702-2604-000001000000}"
	bin, args := msiUninstallByCode(Params{}, code, `C:\t\u.log`)
	if bin != "msiexec.exe" {
		t.Errorf("bin = %q, want msiexec.exe", bin)
	}
	want := `/x {23170F69-40C1-2702-2604-000001000000} /qn /norestart /l*v C:\t\u.log`
	if got := strings.Join(args, " "); got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
}

func TestIsFilelessRegistryUninstall(t *testing.T) {
	code := "{23170F69-40C1-2702-2604-000001000000}"
	cases := []struct {
		name string
		p    Params
		want bool
	}{
		{"uninstall by registry key, no file", Params{Action: "uninstall", UninstallKey: "7-Zip"}, true},
		{"uninstall by display name, no file", Params{Action: "uninstall", DisplayName: "7-Zip"}, true},
		{"uninstall with a file present → file-based", Params{Action: "uninstall", UninstallKey: "7-Zip", FilePath: `C:\p\u.exe`}, false},
		{"MSI by code takes precedence", Params{Action: "uninstall", ProductCode: code, DisplayName: "7-Zip"}, false},
		{"install is never fileless registry", Params{Action: "install", DisplayName: "7-Zip"}, false},
		{"uninstall without key or name", Params{Action: "uninstall"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isFilelessRegistryUninstall(c.p); got != c.want {
				t.Errorf("isFilelessRegistryUninstall = %v, want %v", got, c.want)
			}
		})
	}
}

func TestCommandLineToArgv(t *testing.T) {
	cases := []struct {
		name    string
		cmdline string
		want    []string
	}{
		{"quoted path with silent flag", `"C:\Program Files\7-Zip\Uninstall.exe" /S`,
			[]string{`C:\Program Files\7-Zip\Uninstall.exe`, "/S"}},
		{"unquoted single token", `C:\app\setup.exe`, []string{`C:\app\setup.exe`}},
		{"msiexec uninstall string", `MsiExec.exe /X{23170F69-40C1-2702-2604-000001000000}`,
			[]string{"MsiExec.exe", "/X{23170F69-40C1-2702-2604-000001000000}"}},
		{"multiple args", `"C:\a b\u.exe" --mode silent /q`,
			[]string{`C:\a b\u.exe`, "--mode", "silent", "/q"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := commandLineToArgv(c.cmdline)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("argv = %#v, want %#v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("argv[%d] = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name      string
		p         Params
		exit      int
		success   bool
		reboot    bool
		notApplic bool
	}{
		{"zero is success", Params{}, 0, true, false, false},
		{"3010 success+reboot", Params{}, 3010, true, true, false},
		{"1641 success+reboot", Params{}, 1641, true, true, false},
		{"custom success code", Params{SuccessCodes: []int32{7}}, 7, true, false, false},
		{"1605 on uninstall is not-applicable", Params{Action: "uninstall"}, 1605, false, false, true},
		{"1605 on install is a failure", Params{Action: "install"}, 1605, false, false, false},
		{"1618 busy is a failure", Params{}, 1618, false, false, false},
		{"unknown code fails", Params{}, 9009, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := classify(c.p, c.exit, "")
			if r.Success != c.success || r.RebootRequired != c.reboot || r.NotApplicable != c.notApplic {
				t.Errorf("exit %d → success=%v reboot=%v na=%v; want success=%v reboot=%v na=%v",
					c.exit, r.Success, r.RebootRequired, r.NotApplicable, c.success, c.reboot, c.notApplic)
			}
			if r.Reason == "" {
				t.Errorf("exit %d → empty reason", c.exit)
			}
		})
	}
}
