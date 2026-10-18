package softdist

import (
	"strconv"
	"strings"
)

// CompareVersions compares two dotted version strings ("26.04" vs "25.1.0")
// segment by segment: numerically when both segments are numbers, else as
// case-insensitive text (handles vendor suffixes like "1.2-beta"). Missing
// segments count as zero/empty, so "1.2" == "1.2.0". Returns -1 (a<b), 0, or +1.
func CompareVersions(a, b string) int {
	as := strings.Split(strings.TrimSpace(a), ".")
	bs := strings.Split(strings.TrimSpace(b), ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var sa, sb string
		if i < len(as) {
			sa = strings.TrimSpace(as[i])
		}
		if i < len(bs) {
			sb = strings.TrimSpace(bs[i])
		}
		na, aerr := strconv.Atoi(sa)
		nb, berr := strconv.Atoi(sb)
		switch {
		case aerr == nil && berr == nil:
			if na != nb {
				if na < nb {
					return -1
				}
				return 1
			}
		case sa == "" && berr == nil: // "1.2" vs "1.2.3" → missing = 0
			if nb != 0 {
				return -1
			}
		case sb == "" && aerr == nil:
			if na != 0 {
				return 1
			}
		default:
			if c := strings.Compare(strings.ToLower(sa), strings.ToLower(sb)); c != 0 {
				return c
			}
		}
	}
	return 0
}
