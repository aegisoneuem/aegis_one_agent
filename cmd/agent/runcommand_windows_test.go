//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCommandCMDQuotedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "loop with spaces.txt")
	want := "loop content\r\n"
	if err := os.WriteFile(path, []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	out := runOneCommand(context.Background(), `type "`+path+`"`, "cmd", dir, nil, 1024)
	if out.FullStdoutPath != "" {
		defer os.Remove(out.FullStdoutPath)
	}
	if out.ExitCode != 0 || out.Reason != "" || out.Stdout != want || out.Stderr != "" {
		t.Fatalf("quoted file command: %+v", out)
	}
}

func TestRunCommandCMDSimpleHeredoc(t *testing.T) {
	out := runOneCommand(context.Background(), "echo one & echo two & echo three", "cmd", t.TempDir(), nil, 1024)
	if out.FullStdoutPath != "" {
		defer os.Remove(out.FullStdoutPath)
	}
	if out.ExitCode != 0 || out.Reason != "" || strings.Join(strings.Fields(out.Stdout), " ") != "one two three" {
		t.Fatalf("cmd heredoc: %+v", out)
	}
}
