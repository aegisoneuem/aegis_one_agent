//go:build windows

package patch

import "testing"

func TestClassifyDISMFailure(t *testing.T) {
	cases := []struct {
		name   string
		exit   int
		output string
		want   string
	}{
		{"disk space hresult", 1, "Error: 0x80070070 ...", "insufficient disk space on the target volume"},
		{"disk space phrase", 112, "There is not enough space on the disk.", "insufficient disk space on the target volume"},
		{"source missing", 1, "0x800f081f source files could not be found", "required source files missing (component store may be corrupt — run DISM /RestoreHealth)"},
		{"rolled back", 1, "operation failed 0x800f0922", "install rolled back — a pending operation or a failed servicing step (reboot and retry)"},
		{"not applicable/reboot", 1, "0x800f0923", "package not applicable, or a reboot is pending from a previous update"},
		{"corruption", 1, "0x80073701 ERROR_SXS_ASSEMBLY_MISSING", "component store corruption (run DISM /RestoreHealth or SFC /scannow)"},
		{"access denied", 5, "Access is denied.", "access denied — the agent must run elevated (LocalSystem) to install"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyDISMFailure(c.exit, c.output); got != c.want {
				t.Errorf("classifyDISMFailure(%d, %q) = %q, want %q", c.exit, c.output, got, c.want)
			}
		})
	}
}

func TestClassifyDISMFailureFallback(t *testing.T) {
	if got := classifyDISMFailure(42, "some generic failure with no known code"); got != "install failed (exit 42)" {
		t.Errorf("fallback = %q", got)
	}
	if got := classifyDISMFailure(42, "weird 0xDEADBEEF thing"); got != "install failed (exit 42, 0xDEADBEEF)" {
		t.Errorf("hresult fallback = %q", got)
	}
}

func TestFirstHRESULT(t *testing.T) {
	if got := firstHRESULT("blah 0x800F0922 blah"); got != "0x800F0922" {
		t.Errorf("got %q", got)
	}
	if got := firstHRESULT("no code here"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}
