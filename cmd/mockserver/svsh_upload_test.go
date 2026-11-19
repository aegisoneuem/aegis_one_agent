package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSVSHUploadPaths(t *testing.T) {
	got, err := safeSVSHUploadPath(`reports\daily\win11\status.txt`)
	if err != nil || got != filepath.Join("reports", "daily", "win11", "status.txt") {
		t.Fatalf("path=%q err=%v", got, err)
	}
	for _, name := range []string{`../outside`, `reports/../../outside`, `C:\temp\file`, `reports/file:stream`, `reports/.. /file`, ""} {
		if _, err := safeSVSHUploadPath(name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
}

func TestUploadDoesNotOverwriteEarlierReport(t *testing.T) {
	dir := t.TempDir()
	f, first, err := createUploadFile(dir, "status.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("original")
	f.Close()
	f, second, err := createUploadFile(dir, "status.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	data, err := os.ReadFile(first)
	if err != nil || string(data) != "original" || first == second {
		t.Fatalf("original changed: %q, %v", data, err)
	}
}

func TestPullQuotedPaths(t *testing.T) {
	p, name := splitPullArgs(`"C:\temp\with spaces.bin" "copy.bin"`)
	if p != `C:\temp\with spaces.bin` || name != "copy.bin" {
		t.Fatalf("%q %q", p, name)
	}
	if p, _ := splitPullArgs(`C:\temp\*.txt`); p != "" {
		t.Fatal("pull accepted wildcard for a single destination")
	}
}
