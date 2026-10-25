//go:build windows

package softdist

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBatchPayloadSpacedPathAndParameters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Payload With Spaces")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "custom-remove.cmd")
	if err := os.WriteFile(path, []byte("@echo off\r\necho tag=%~1\r\necho env=%AEGIS_PARAM_TAG%\r\nexit /b 0\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sum, err := fileHash(path, "sha256")
	if err != nil {
		t.Fatal(err)
	}
	r := Run(context.Background(), Params{Action: "uninstall", InstallerType: "cmd", FilePath: path, ExpectedHash: sum, Timeout: 5 * time.Second, SilentArgs: `"${TAG}"`, Parameters: map[string]string{"TAG": "daily release"}})
	if !r.Success || !strings.Contains(r.Detail, "tag=daily release") || !strings.Contains(r.Detail, "env=daily release") {
		t.Fatalf("batch launch failed: %+v", r)
	}
}
