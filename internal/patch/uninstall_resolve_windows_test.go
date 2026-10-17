//go:build windows

package patch

import "testing"

func TestPackageVersion(t *testing.T) {
	cases := map[string]string{
		"Package_for_RollupFix~31bf3856ad364e35~amd64~~20348.587.1.7":  "20348.587.1.7",
		"Package_for_KB5027397~31bf3856ad364e35~amd64~~22621.2355.1.1": "22621.2355.1.1",
		"NoTilde": "",
	}
	for in, want := range cases {
		if got := packageVersion(in); got != want {
			t.Errorf("packageVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"20348.587.1.7", "20348.1016.1.0", true},   // minor bump
		{"20348.1016.1.0", "20348.587.1.7", false},  // reverse
		{"22621.2355.1.1", "22621.2355.1.1", false}, // equal
		{"22621.7517.1.20", "22621.7584.1.0", true}, // real LCU supersede
		{"1.2", "1.2.0.1", true},                    // shorter is less when longer extends
	}
	for _, c := range cases {
		if got := versionLess(c.a, c.b); got != c.want {
			t.Errorf("versionLess(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestMaxVersionPackage(t *testing.T) {
	ids := []string{
		"Package_for_RollupFix~31bf3856ad364e35~amd64~~22621.7517.1.20",
		"Package_for_RollupFix~31bf3856ad364e35~amd64~~22621.7584.1.0",
		"Package_for_RollupFix~31bf3856ad364e35~amd64~~22621.3952.1.1",
	}
	want := "Package_for_RollupFix~31bf3856ad364e35~amd64~~22621.7584.1.0"
	if got := maxVersionPackage(ids); got != want {
		t.Errorf("maxVersionPackage = %q, want %q", got, want)
	}
	if got := maxVersionPackage(nil); got != "" {
		t.Errorf("maxVersionPackage(nil) = %q, want empty", got)
	}
}
