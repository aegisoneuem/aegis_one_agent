package main

import "testing"

func TestVerbTakesPath(t *testing.T) {
	yes := []string{"cd", "cat", "grab", "pull", "copy", "cp", "move", "mv", "delete", "del", "rm", "mkdir", "md", "touch"}
	for _, v := range yes {
		if !verbTakesPath(v) {
			t.Errorf("verbTakesPath(%q) = false, want true", v)
		}
	}
	no := []string{"help", "hosts", "run", "exec", "shell", "fanout", "use", "history", "set", "", "inventory"}
	for _, v := range no {
		if verbTakesPath(v) {
			t.Errorf("verbTakesPath(%q) = true, want false", v)
		}
	}
}

func TestSplitDirAndPartial(t *testing.T) {
	cases := []struct {
		in, wantDir, wantPartial string
	}{
		{"", "", ""},
		{"foo", "", "foo"},
		{`C:\temp`, `C:\`, "temp"},
		{`C:\temp\`, `C:\temp\`, ""},
		{`C:\temp\x.txt`, `C:\temp\`, "x.txt"},
		{`C:/temp/sub/x`, `C:/temp/sub/`, "x"},
	}
	for _, c := range cases {
		gotDir, gotPartial := splitDirAndPartial(c.in)
		if gotDir != c.wantDir || gotPartial != c.wantPartial {
			t.Errorf("splitDirAndPartial(%q) = (%q,%q), want (%q,%q)", c.in, gotDir, gotPartial, c.wantDir, c.wantPartial)
		}
	}
}

func TestJoinWithSep(t *testing.T) {
	cases := []struct {
		dir, name, orig, want string
	}{
		{``, `foo`, `foo`, `foo`},
		{`C:\temp\`, `x.txt`, `C:\temp\x`, `C:\temp\x.txt`},
		{`C:\temp`, `x.txt`, `C:\temp\x`, `C:\temp\x.txt`},
		{`C:/temp/`, `x.txt`, `C:/temp/x`, `C:/temp/x.txt`},
	}
	for _, c := range cases {
		got := joinWithSep(c.dir, c.name, c.orig)
		if got != c.want {
			t.Errorf("joinWithSep(%q,%q,%q) = %q, want %q", c.dir, c.name, c.orig, got, c.want)
		}
	}
}
