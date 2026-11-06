package appxinventory

import "testing"

func TestParsePackageFullName(t *testing.T) {
	cases := []struct {
		full                                  string
		name, version, arch, resID, publisher string
	}{
		// Real examples read from the live registry.
		{"aimgr_0.20.63.0_x64__8wekyb3d8bbwe",
			"aimgr", "0.20.63.0", "x64", "", "8wekyb3d8bbwe"},
		{"1527c705-839a-4832-9118-54d4Bd6a0c89_10.0.19640.1000_neutral_neutral_cw5n1h2txyewy",
			"1527c705-839a-4832-9118-54d4Bd6a0c89", "10.0.19640.1000", "neutral", "neutral", "cw5n1h2txyewy"},
		{"Microsoft.WindowsStore_22210.1401.6.0_x64__8wekyb3d8bbwe",
			"Microsoft.WindowsStore", "22210.1401.6.0", "x64", "", "8wekyb3d8bbwe"},
	}
	for _, c := range cases {
		name, version, arch, resID, pub := parsePackageFullName(c.full)
		if name != c.name || version != c.version || arch != c.arch || resID != c.resID || pub != c.publisher {
			t.Errorf("parse(%q) = (%q,%q,%q,%q,%q), want (%q,%q,%q,%q,%q)",
				c.full, name, version, arch, resID, pub, c.name, c.version, c.arch, c.resID, c.publisher)
		}
	}
}

// Break: malformed / short / empty full names yield empty parts (no panic, no
// bogus identity).
func TestParsePackageFullName_Break(t *testing.T) {
	for _, bad := range []string{"", "   ", "nounderscores", "only_three_parts", "a_b_c_d"} {
		if name, _, _, _, _ := parsePackageFullName(bad); name != "" {
			t.Errorf("parse(%q) should yield empty name, got %q", bad, name)
		}
	}
}

func TestPackageFamilyName(t *testing.T) {
	if got := packageFamilyName("Microsoft.WindowsStore", "8wekyb3d8bbwe"); got != "Microsoft.WindowsStore_8wekyb3d8bbwe" {
		t.Errorf("family = %q", got)
	}
	if got := packageFamilyName("", "pub"); got != "" {
		t.Errorf("empty name should give empty family, got %q", got)
	}
	if got := packageFamilyName("name", ""); got != "" {
		t.Errorf("empty publisher should give empty family, got %q", got)
	}
}

func TestIsResourceSatellite(t *testing.T) {
	// Satellites (dropped):
	for _, full := range []string{
		"AppUp.Intel_2537.8.7.0_neutral_split.scale-100_8wekyb3d8bbwe",
		"Microsoft.Photos_1.0_neutral_split.language-en-us_8wekyb3d8bbwe",
	} {
		if !isResourceSatellite(full) {
			t.Errorf("%q should be a resource satellite", full)
		}
	}
	// Main packages (kept): empty or "neutral" resourceId.
	for _, full := range []string{
		"AppUp.Intel_2537.8.7.0_x64__8wekyb3d8bbwe",
		"1527c705-x_10.0.19640.1000_neutral_neutral_cw5n1h2txyewy",
		"Microsoft.WindowsStore_22210.1401.6.0_x64__8wekyb3d8bbwe",
	} {
		if isResourceSatellite(full) {
			t.Errorf("%q should NOT be a resource satellite", full)
		}
	}
}

func TestOriginString(t *testing.T) {
	cases := map[int64]string{2: "Inbox", 3: "Store", 6: "LineOfBusiness", 0: "Unknown", 99: "Unknown"}
	for in, want := range cases {
		if got := originString(in); got != want {
			t.Errorf("originString(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestDedupePackages(t *testing.T) {
	// One app with a neutral bundle row (no DisplayName) + an x64 row (with
	// DisplayName), same version → collapse to the x64/DisplayName row.
	in := []AppxPackage{
		{PackageFullName: "Dolby_3.27_neutral__pub", PackageFamilyName: "Dolby_pub", Version: "3.27", Architecture: "neutral"},
		{PackageFullName: "Dolby_3.27_x64__pub", PackageFamilyName: "Dolby_pub", Version: "3.27", Architecture: "x64", DisplayName: "Dolby Access"},
		// A genuinely different app stays.
		{PackageFullName: "Claude_1.8_x64__p2", PackageFamilyName: "Claude_p2", Version: "1.8", Architecture: "x64", DisplayName: "Claude"},
	}
	out := dedupePackages(in)
	if len(out) != 2 {
		t.Fatalf("got %d packages, want 2 (Dolby collapsed + Claude): %+v", len(out), out)
	}
	for _, p := range out {
		if p.PackageFamilyName == "Dolby_pub" {
			if p.DisplayName != "Dolby Access" || p.Architecture != "x64" {
				t.Errorf("Dolby canonical row wrong: %+v", p)
			}
		}
	}
}

func TestPreferAppx(t *testing.T) {
	withDN := AppxPackage{DisplayName: "X", Architecture: "neutral"}
	noDN := AppxPackage{Architecture: "x64"}
	if !preferAppx(withDN, noDN) {
		t.Error("DisplayName should win over no-DisplayName even if other is x64")
	}
	x64 := AppxPackage{Architecture: "x64"}
	neutral := AppxPackage{Architecture: "neutral"}
	if !preferAppx(x64, neutral) {
		t.Error("x64 should win over neutral when neither has DisplayName")
	}
}

func TestBuildPackage(t *testing.T) {
	p, ok := buildPackage("Microsoft.WindowsStore_22210.1401.6.0_x64__8wekyb3d8bbwe", `C:\Program Files\WindowsApps\...`, 3)
	if !ok {
		t.Fatal("expected ok")
	}
	if p.Name != "Microsoft.WindowsStore" || p.Version != "22210.1401.6.0" || p.Architecture != "x64" {
		t.Errorf("bad parse: %+v", p)
	}
	if p.PackageFamilyName != "Microsoft.WindowsStore_8wekyb3d8bbwe" {
		t.Errorf("family = %q", p.PackageFamilyName)
	}
	if p.Origin != "Store" {
		t.Errorf("origin = %q", p.Origin)
	}
	// Break: unparseable full name -> dropped.
	if _, ok := buildPackage("garbage", "", 0); ok {
		t.Error("unparseable full name should not build")
	}
}
