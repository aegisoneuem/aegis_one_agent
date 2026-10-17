//go:build windows

package patch

import "testing"

// sampleTable is a trimmed real `dism /online /get-packages /format:table` output
// (from a Win11 22631 machine) covering the cases that matter: a standalone KB
// package, an LCU chain (Superseded + Installed RollupFix), an SSU, and non-update
// rows that must be ignored.
const sampleTable = `
Deployment Image Servicing and Management tool
Version: 10.0.22621.2792

Image Version: 10.0.22631.7584

Packages listing:


------------------------------------------------------------------------- | ---------- | --------------- | ----------------
Package Identity                                                          | State      | Release Type    | Install Time
------------------------------------------------------------------------- | ---------- | --------------- | ----------------
Microsoft-Windows-Hello-Face-Package~31bf3856ad364e35~amd64~~10.0.22621.7582 | Installed  | OnDemand Pack   | 10-10-2026 05:48
Package_for_DotNetRollup_481~31bf3856ad364e35~amd64~~10.0.9346.1          | Installed  | Update          | 10-10-2026 05:48
Package_for_KB5027397~31bf3856ad364e35~amd64~~22621.2355.1.1              | Installed  | Update          | 04-12-2023 06:15
Package_for_KB5041655~31bf3856ad364e35~amd64~~22621.3952.1.1              | Installed  | Update          | 23-09-2024 12:02
Package_for_RollupFix~31bf3856ad364e35~amd64~~22621.7517.1.20             | Superseded | Security Update | 09-09-2026 08:40
Package_for_RollupFix~31bf3856ad364e35~amd64~~22621.7584.1.0              | Installed  | Update          | 10-10-2026 05:48
Package_for_ServicingStack_7578~31bf3856ad364e35~amd64~~22621.7578.1.3    | Installed  | Security Update | 05-10-2026 07:08

The operation completed successfully.
`

func TestParseCBSPackageTable(t *testing.T) {
	pkgs := parseCBSPackageTable(sampleTable)
	if len(pkgs) != 7 {
		t.Fatalf("parsed %d packages, want 7: %#v", len(pkgs), pkgs)
	}
	if pkgs[2].Identity != "Package_for_KB5027397~31bf3856ad364e35~amd64~~22621.2355.1.1" || pkgs[2].State != "Installed" {
		t.Errorf("row 2 = %#v", pkgs[2])
	}
	if pkgs[4].State != "Superseded" {
		t.Errorf("superseded RollupFix state = %q", pkgs[4].State)
	}
}

func TestMatchCBSPackage(t *testing.T) {
	pkgs := parseCBSPackageTable(sampleTable)
	cases := []struct {
		name      string
		kb        string
		osBuild   string
		wantID    string
		wantFound bool
	}{
		{"standalone KB by name", "5027397", "", "Package_for_KB5027397~31bf3856ad364e35~amd64~~22621.2355.1.1", true},
		{"LCU by build picks Installed over Superseded", "5999999", "22621.7584", "Package_for_RollupFix~31bf3856ad364e35~amd64~~22621.7584.1.0", true},
		{"superseded LCU by its build still matches (reported not removable)", "5999999", "22621.7517", "Package_for_RollupFix~31bf3856ad364e35~amd64~~22621.7517.1.20", true},
		{"unknown KB without build → no match (WUA fallback)", "5999999", "", "", false},
		{"build must not match SSU or FoD rows", "5999999", "22621.7578", "", false},
		{"KB prefix must be exact (no partial)", "502739", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, found := matchCBSPackage(pkgs, c.kb, c.osBuild)
			if found != c.wantFound {
				t.Fatalf("found = %v, want %v (got %#v)", found, c.wantFound, got)
			}
			if found && got.Identity != c.wantID {
				t.Errorf("identity = %q, want %q", got.Identity, c.wantID)
			}
		})
	}
}

func TestClassifyDISMRemoveFailure(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string // substring the reason must contain
	}{
		{"permanent package refused", "Error: 0x800f0825\nPackage cannot be uninstalled.", "permanent"},
		{"pending reboot blocks", "Error: 0x800f0904", "reboot"},
		{"access denied", "Error: 0x80070005 Access is denied.", "access denied"},
		{"unknown hresult surfaces", "Error: 0x80073701", "0x80073701"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyDISMRemoveFailure(1, c.out)
			if !containsFold(got, c.want) {
				t.Errorf("reason %q does not mention %q", got, c.want)
			}
		})
	}
}

func containsFold(s, sub string) bool {
	return len(sub) == 0 || len(s) >= len(sub) && (stringsIndexFold(s, sub) >= 0)
}

func stringsIndexFold(s, sub string) int {
	ls, lsub := []rune(s), []rune(sub)
	for i := 0; i+len(lsub) <= len(ls); i++ {
		match := true
		for j := range lsub {
			a, b := ls[i+j], lsub[j]
			if 'A' <= a && a <= 'Z' {
				a += 32
			}
			if 'A' <= b && b <= 'Z' {
				b += 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
