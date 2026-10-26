//go:build windows

package softdist

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

const privateTestValue = "private-test-canary-92a4"

// Real child process: checks stdin delivery, absence from argv/environment,
// ordinary parameter propagation, then deliberately prints transformed secrets.
func TestPrivateInstallerChild(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "--private-installer-child" {
		return
	}
	for _, s := range append(os.Args, os.Environ()...) {
		if strings.Contains(s, privateTestValue) {
			os.Exit(71)
		}
	}
	if os.Getenv("AEGIS_PARAM_REGION") != "apac" || os.Getenv("AEGIS_SECRET_INPUT") != "stdin-json-v1" {
		os.Exit(72)
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(73)
	}
	var input struct {
		Version    int               `json:"version"`
		Parameters map[string]string `json:"secure_parameters"`
	}
	if json.Unmarshal(raw, &input) != nil || input.Version != 1 || input.Parameters["API_TOKEN"] != privateTestValue {
		os.Exit(74)
	}
	fmt.Println(privateTestValue, base64.StdEncoding.EncodeToString([]byte(privateTestValue)))
	fmt.Fprintln(os.Stderr, privateTestValue)
	os.Exit(0)
}
func TestPrivateInstallerPipeAndOutputSuppression(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	p := Params{Parameters: map[string]string{"REGION": "apac"}, SecureParameters: map[string]string{"API_TOKEN": privateTestValue}, Timeout: 10 * time.Second}
	result := execInstaller(context.Background(), p, exe, []string{"-test.run=^TestPrivateInstallerChild$", "--", "--private-installer-child"}, "")
	if !result.Success {
		t.Fatalf("private child failed: %+v", result)
	}
	text := logs.String() + result.Reason + result.Detail
	if strings.Contains(text, privateTestValue) || strings.Contains(text, base64.StdEncoding.EncodeToString([]byte(privateTestValue))) {
		t.Fatal("secret leaked into logs/results")
	}
	if !strings.Contains(result.Detail, "suppressed") {
		t.Fatal("missing explicit output suppression notice")
	}
}
func TestActiveSessionSelection(t *testing.T) {
	id := uint32(8)
	for _, active := range [][]uint32{nil, {3, 8}} {
		if _, err := chooseSession(active, nil); err == nil {
			t.Fatal("ambiguous/missing session accepted")
		}
	}
	if n, err := chooseSession([]uint32{3, 8}, &id); err != nil || n != 8 {
		t.Fatal("explicit session not selected")
	}
	if _, err := chooseSession([]uint32{3}, &id); err == nil {
		t.Fatal("offline session accepted")
	}
}

func TestRegistryUninstallUsesTargetEnvironment(t *testing.T) {
	s := &Session{environment: []string{`LOCALAPPDATA=C:\Users\Target\AppData\Local`, `USERPROFILE=C:\Users\Target`}}
	app := &InstalledApp{QuietUninstallString: `"%localappdata%\Vendor\remove.exe" /quiet`}
	bin, _, err := registryUninstallInvocation(Params{Session: s}, app, "")
	if err != nil || bin != `C:\Users\Target\AppData\Local\Vendor\remove.exe` {
		t.Fatalf("target expansion: %q %v", bin, err)
	}
}
