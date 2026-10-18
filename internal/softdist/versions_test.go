package softdist

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"25.01", "26.04", -1},
		{"26.04", "25.01", 1},
		{"26.04", "26.04", 0},
		{"1.2", "1.2.0", 0},
		{"1.2", "1.2.1", -1},
		{"1.10", "1.9", 1}, // numeric, not lexical
		{"10.0.19041", "10.0.22631", -1},
		{"1.2-beta", "1.2-rc", -1}, // non-numeric falls back to text
		{"", "1.0", -1},
		{" 26.04 ", "26.04", 0},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
