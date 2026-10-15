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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bin, args, err := buildInvocation(c.p, c.abs)
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
	if _, _, err := buildInvocation(Params{InstallerType: "rpm"}, `C:\p\x`); err == nil {
		t.Fatal("expected an error for an unknown installer_type")
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
