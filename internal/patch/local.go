package patch

import "time"

// LocalState is the lightweight, network-free view of a device's patch level:
// its OS build and the set of installed update KBs. This is the PRIMARY
// patch-detection signal — the agent reports it and the server diffs it against
// its catalog to compute "missing". It is cheap to collect (registry only, no
// COM/WUA) so it can run every check-in cycle.
type LocalState struct {
	ProductName    string    // e.g. "Windows 11 Pro"
	DisplayVersion string    // e.g. "24H2"
	BuildNumber    string    // e.g. "26100"
	UBR            int       // Update Build Revision, e.g. 4652
	FullBuild      string    // "26100.4652" — the load-bearing signal for OS cumulative level
	Architecture   string    // "amd64" | "arm64" | "x86" — for server catalog matching
	InstalledKBs   []string  // sorted, deduped, e.g. ["KB5034123", ...] (best-effort supplement)
	CollectedAt    time.Time
}

// extractKB pulls the "KB<digits>" token out of a CBS package key name such as
// "Package_for_KB5034123~31bf3856ad364e35~amd64~~10.0.1.0". Returns "" when the
// name has no KB token.
func extractKB(packageName string) string {
	i := indexKB(packageName)
	if i < 0 {
		return ""
	}
	j := i + 2
	for j < len(packageName) && packageName[j] >= '0' && packageName[j] <= '9' {
		j++
	}
	if j == i+2 {
		return "" // "KB" not followed by any digit
	}
	return packageName[i:j]
}

// indexKB returns the index of the first "KB" immediately followed by a digit,
// or -1. (A plain strings.Index would also match "KB" in a hash segment.)
func indexKB(s string) int {
	for i := 0; i+2 < len(s); i++ {
		if s[i] == 'K' && s[i+1] == 'B' && s[i+2] >= '0' && s[i+2] <= '9' {
			return i
		}
	}
	return -1
}

// parseBuildInt reads the leading run of digits of s as an int (0 if none).
func parseBuildInt(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			break
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}
