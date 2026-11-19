package main

import (
	"strings"
	"testing"
)

func TestHeredocReaderPreservesCommentsAndBlankLines(t *testing.T) {
	s := &svshSession{}
	s.engine = newScriptEngine(s)
	s.readLoop(strings.NewReader("script powershell until EOT\n# script comment\n\nWrite-Output hi\n"), false)
	if !s.engine.core.State().InHeredoc || s.prompt() != "... [EOT]> " {
		t.Fatalf("not collecting heredoc: %q", s.prompt())
	}
}

func TestNestedHeredocPreservesCommentsAndBlankLines(t *testing.T) {
	s := &svshSession{}
	s.engine = newScriptEngine(s)
	s.readLoop(strings.NewReader("for X in 1\nscript powershell until EOT\n# script comment\n\nWrite-Output hi\nEOT\n"), false)
	if s.engine.core.State().BlockDepth != 1 {
		t.Fatal("nested block was not retained")
	}
}
