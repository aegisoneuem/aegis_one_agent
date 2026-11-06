package patch

import (
	"reflect"
	"testing"
)

func TestNormKBDigits(t *testing.T) {
	cases := map[string]string{
		"KB5034123":  "5034123",
		"kb5034123":  "5034123",
		"5034123":    "5034123",
		"  KB5034123 ": "5034123",
		"KB5034123~extra": "5034123",
		"":           "",
		"KB":         "",
		"notakb":     "",
		"KBabc":      "",
	}
	for in, want := range cases {
		if got := normKBDigits(in); got != want {
			t.Errorf("normKBDigits(%q) = %q, want %q", in, got, want)
		}
	}
}

// Union is the "nothing missed" guarantee: KBs from any source survive, in
// canonical KB<digits> form, deduped and sorted — even when sources overlap or
// format differently.
func TestUnionKBs(t *testing.T) {
	wua := []string{"5041655", "KB5126652", "890830"}  // bare + prefixed
	qfe := []string{"KB5041655", "KB5007651"}          // overlaps wua (5041655)
	cbs := []string{"KB5126652", "KB5027397"}          // overlaps wua (5126652)

	got := unionKBs(wua, qfe, cbs)
	want := []string{"KB5007651", "KB5027397", "KB5041655", "KB5126652", "KB890830"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unionKBs = %v, want %v", got, want)
	}
}

// Break: empty inputs, junk, and all-overlap collapse cleanly (no panics, no
// dupes, no empty entries).
func TestUnionKBs_Break(t *testing.T) {
	if got := unionKBs(nil, nil); len(got) != 0 {
		t.Errorf("union of nils = %v, want empty", got)
	}
	if got := unionKBs([]string{"", "KB", "junk", "   "}); len(got) != 0 {
		t.Errorf("union of junk = %v, want empty", got)
	}
	// Same KB in three formats collapses to one.
	got := unionKBs([]string{"KB5034123"}, []string{"5034123"}, []string{"kb5034123"})
	if len(got) != 1 || got[0] != "KB5034123" {
		t.Errorf("same KB in 3 formats = %v, want [KB5034123]", got)
	}
}
