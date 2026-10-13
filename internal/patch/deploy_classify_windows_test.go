//go:build windows

package patch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeHashAlgo(t *testing.T) {
	cases := map[string]string{"": "sha256", "SHA256": "sha256", "sha-256": "sha256", "SHA-512": "sha512", "sha512": "sha512"}
	for in, want := range cases {
		if got := normalizeHashAlgo(in); got != want {
			t.Errorf("normalizeHashAlgo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFileHash(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Known digests of "abc".
	wantSHA256 := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	wantSHA512 := "ddaf35a193617abacc417349ae20413112e6fa4e89a97ea20a9eeee64b55d39a2192992a274fc1a836ba3c23a3feebbd454d4423643ce80e2a9ac94fa54ca49f"
	if got, _ := fileHash(p, "sha256"); got != wantSHA256 {
		t.Errorf("sha256 = %q", got)
	}
	if got, _ := fileHash(p, "sha512"); got != wantSHA512 {
		t.Errorf("sha512 = %q", got)
	}
	if _, err := fileHash(p, "md5"); err == nil {
		t.Error("md5 should be unsupported")
	}
}

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
		{"rolled back", 1, "operation failed 0x800f0922", "install rolled back — a pending operation or a failed servicing step (retry may help)"},
		{"not applicable/reboot", 1, "0x800f0923", "package not applicable, or a reboot is pending from a previous update"},
		{"corruption", 1, "0x80073701 ERROR_SXS_ASSEMBLY_MISSING", "component store corruption (run DISM /RestoreHealth or SFC /scannow)"},
		{"access denied", 5, "Access is denied.", "access denied — the agent must run elevated (LocalSystem) to install"},
		{"not supported exit 50", 50, "Error: 50\r\n\r\nThe request is not supported.", "DISM does not support installing this .msu (exit 50, 'not supported') — handled by the wusa fallback"},
		{"not supported hresult", 1, "0x80070032 the request is not supported", "DISM does not support installing this .msu (exit 50, 'not supported') — handled by the wusa fallback"},
		{"not applicable 081e", 1, "0x800f081e CBS_E_NOT_APPLICABLE", "patch not applicable to this system (already superseded, or not for this OS/build)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, _ := classifyDISMFailure(c.exit, c.output); got != c.want {
				t.Errorf("classifyDISMFailure(%d, %q) = %q, want %q", c.exit, c.output, got, c.want)
			}
		})
	}
}

func TestClassifyDISMFailureTransient(t *testing.T) {
	// Permanent (do not retry).
	if _, tr := classifyDISMFailure(1, "0x80070070 disk"); tr {
		t.Error("disk space should not be transient")
	}
	if _, tr := classifyDISMFailure(5, "access is denied"); tr {
		t.Error("access denied should not be transient")
	}
	if _, tr := classifyDISMFailure(50, "The request is not supported."); tr {
		t.Error("not-supported (exit 50) should not be transient")
	}
	// Transient (retry) — only the explicitly-enumerated glitches.
	if _, tr := classifyDISMFailure(1, "another installation is already in progress"); !tr {
		t.Error("busy should be transient")
	}
	if _, tr := classifyDISMFailure(1, "operation failed 0x800f0922"); !tr {
		t.Error("rolled-back should be transient")
	}
	// Unknown failures are now treated as PERMANENT (no blind retry).
	if _, tr := classifyDISMFailure(1, "some unknown failure"); tr {
		t.Error("unknown should NOT be transient (fail fast with a clear code)")
	}
}

func TestClassifyDISMFailureFallback(t *testing.T) {
	if got, _ := classifyDISMFailure(42, "some generic failure with no known code"); got != "install failed (exit 42)" {
		t.Errorf("fallback = %q", got)
	}
	if got, _ := classifyDISMFailure(42, "weird 0xDEADBEEF thing"); got != "install failed (exit 42, 0xDEADBEEF)" {
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
