package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B.6 integration test — exercise the WIRED path: a real nshSession,
// real s.exec(), real runWithRedirect flow. We use the `help` verb
// because it is pure stdout, no dispatch to agents, no live stream
// required.

func newTestSession(t *testing.T, out io.Writer) *nshSession {
	t.Helper()
	srv := &server{}
	return &nshSession{
		srv:    srv,
		shell:  "cmd",
		output: out,
	}
}

// Local-to-mock redirect: `help > C:\tmp\help.txt`. The file on disk
// must contain the help text; the terminal (out) must stay quiet.
func TestNSHRedirect_Integration_LocalFile(t *testing.T) {
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "help.txt")

	var stdout bytes.Buffer
	s := newTestSession(t, &stdout)

	cont := s.exec("help > " + dst)
	if !cont {
		t.Error("exec returned false on help; want true")
	}

	// Dest file has content.
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if len(data) == 0 {
		t.Error("redirect wrote empty file; expected help text")
	}
	if !strings.Contains(string(data), "run") || !strings.Contains(string(data), "hosts") {
		t.Errorf("file missing expected help content (first 200 bytes): %q", data[:min(len(data), 200)])
	}

	// Terminal received none of the help text (output went to buffer).
	if strings.Contains(stdout.String(), "VERBS") || strings.Contains(stdout.String(), "SESSION") {
		t.Errorf("terminal captured help text that should have been redirected: %q", stdout.String())
	}
}

// Append mode: run twice, file length grows.
func TestNSHRedirect_Integration_AppendGrows(t *testing.T) {
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "log.txt")
	s := newTestSession(t, io.Discard)

	s.exec("help > " + dst)
	fi1, _ := os.Stat(dst)
	size1 := fi1.Size()

	s.exec("help >> " + dst)
	fi2, _ := os.Stat(dst)
	size2 := fi2.Size()

	if size2 <= size1 {
		t.Errorf("append did not grow file: %d -> %d", size1, size2)
	}
}

// Overwrite mode: run twice, file length stable.
func TestNSHRedirect_Integration_OverwriteStable(t *testing.T) {
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "stable.txt")
	s := newTestSession(t, io.Discard)

	s.exec("help > " + dst)
	fi1, _ := os.Stat(dst)
	size1 := fi1.Size()

	s.exec("help > " + dst)
	fi2, _ := os.Stat(dst)
	size2 := fi2.Size()

	if size1 != size2 {
		t.Errorf("overwrite changed file size: %d -> %d", size1, size2)
	}
}

// BREAK: redirect with empty verb output produces an empty file, not a
// crash. The `hosts` verb with no connected agents prints a header
// line, so use `pwd` (prints the current working dir which may be empty).
func TestNSHRedirect_Integration_Break_EmptyOutputCreatesFile(t *testing.T) {
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "empty.txt")
	s := newTestSession(t, io.Discard)
	// pwd with no targets prints nothing; a path that produces no bytes
	// still creates the file so the operator sees "I ran it".
	_ = s.exec("pwd > " + dst)
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("empty redirect did not create destination: %v", err)
	}
}

// BREAK: `cat ... > ...` is exempt from the NSH pre-parser — the cat
// verb's own parser handles it. We can't test the full cat path here
// without agents, but we CAN assert that the pre-parser returns
// Redirect=false so the cat verb gets the raw line.
func TestNSHRedirect_Integration_Break_CatBypassesPreparser(t *testing.T) {
	// Direct parser test (already covered) but also verify at the exec
	// layer by observing that no destination file is created for the
	// NSH pre-parser's handling — only cat's own handling would (and
	// it fails without a configured agent).
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "cat-handled.txt")
	s := newTestSession(t, io.Discard)
	_ = s.exec("cat C:\\nonexistent-src.txt > " + dst)
	// Since no agent is configured, cat's remote read fails; what we
	// are asserting here is that the NSH pre-parser did NOT create an
	// empty destination file via its own path. If it had, the file
	// would exist (even if empty).
	if _, err := os.Stat(dst); err == nil {
		t.Errorf("NSH pre-parser handled a cat line (dst file created); cat should own its own redirect semantics")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
