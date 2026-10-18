package softdist

import (
	"sort"
	"strings"
	"testing"
)

func TestSubstitute(t *testing.T) {
	params := map[string]string{
		"hostname":     "WIN-LAB01",
		"download_dir": `C:\SKVC\download`,
		"LICENSE":      "ABC-123",
	}
	cases := []struct{ in, want string }{
		{"/HOST=${hostname}", "/HOST=WIN-LAB01"},
		{"/CONFIG=${download_dir}\\app.cfg", `/CONFIG=C:\SKVC\download\app.cfg`},
		{"/KEY=${LICENSE} /HOST=${hostname}", "/KEY=ABC-123 /HOST=WIN-LAB01"},
		{"/X{23170F69-40C1-2702}", "/X{23170F69-40C1-2702}"}, // MSI GUID untouched (no $)
		{"/unknown=${nope}", "/unknown=${nope}"},             // unknown token left verbatim
		{"no tokens here", "no tokens here"},
		{"${hostname", "${hostname"}, // no closing brace
		{"", ""},
	}
	for _, c := range cases {
		if got := Substitute(c.in, params); got != c.want {
			t.Errorf("Substitute(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := Substitute("/HOST=${hostname}", nil); got != "/HOST=${hostname}" {
		t.Errorf("nil params should pass through, got %q", got)
	}
}

func TestParamEnv(t *testing.T) {
	env := paramEnv(map[string]string{"download_dir": `C:\d`, "License-Key": "X1"})
	sort.Strings(env)
	want := []string{`AEGIS_PARAM_DOWNLOAD_DIR=C:\d`, "AEGIS_PARAM_LICENSE_KEY=X1"}
	if strings.Join(env, "|") != strings.Join(want, "|") {
		t.Errorf("paramEnv = %v, want %v", env, want)
	}
	if paramEnv(nil) != nil {
		t.Error("paramEnv(nil) should be nil")
	}
}
