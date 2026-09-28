package patch

import "testing"

func TestExtractKB(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Package_for_KB5034123~31bf3856ad364e35~amd64~~10.0.1.0", "KB5034123"},
		{"Package_for_RollupFix~31bf3856ad364e35~amd64~~26100.4652.1.10", ""},
		{"Microsoft-Windows-Client-Features-Package~31bf3856ad364e35~amd64", ""},
		{"KB12345", "KB12345"},
		{"prefixKB999suffix", "KB999"},
		{"Package_for_KB5034441~x~KB5034123", "KB5034441"}, // first match wins
		{"KB", ""},
		{"KBabc", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := extractKB(c.in); got != c.want {
			t.Errorf("extractKB(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseBuildInt(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"26100", 26100},
		{"22000", 22000},
		{"19045", 19045},
		{"26100.4652", 26100},
		{"", 0},
		{"abc", 0},
	}
	for _, c := range cases {
		if got := parseBuildInt(c.in); got != c.want {
			t.Errorf("parseBuildInt(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
