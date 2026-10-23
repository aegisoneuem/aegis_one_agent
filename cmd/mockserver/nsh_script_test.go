package main

import (
	"strings"
	"testing"
)

func TestHeredocReaderPreservesCommentsAndBlankLines(t *testing.T) {
	s := &nshSession{}
	s.engine = newScriptEngine(s)
	s.readLoop(strings.NewReader("script powershell until EOT\n# script comment\n\nWrite-Output hi\n"), false)
	if !s.engine.inHeredoc || s.prompt() != "... [EOT]> " {
		t.Fatalf("not collecting heredoc: %q", s.prompt())
	}
	if got := strings.Join(s.engine.heredocLines, "\n"); got != "# script comment\n\nWrite-Output hi" {
		t.Fatalf("body changed: %q", got)
	}
}

func TestNestedHeredocPreservesCommentsAndBlankLines(t *testing.T) {
	s := &nshSession{}
	s.engine = newScriptEngine(s)
	s.readLoop(strings.NewReader("for X in 1\nscript powershell until EOT\n# script comment\n\nWrite-Output hi\nEOT\n"), false)
	got := strings.Join(s.engine.blocks[0].body, "\n")
	if !strings.Contains(got, "# script comment\n\nWrite-Output hi") {
		t.Fatalf("body changed: %q", got)
	}
}
