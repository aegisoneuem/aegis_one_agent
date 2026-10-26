//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedBundleToV2Task(t *testing.T) {
	fixture := deploymentSecrets{Version: 1, Username: "FixtureUser", Domain: "TESTHOST", Password: []byte("account-canary-938"), Parameters: map[string]string{"API_TOKEN": "installer-canary-267"}}
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	sealed, err := secure.ProtectUserData(raw)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "credentials.dpapi")
	if err = os.WriteFile(path, sealed, 0600); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, fixture.Password) || bytes.Contains(sealed, []byte(fixture.Parameters["API_TOKEN"])) {
		t.Fatal("plaintext credential persisted")
	}
	env := map[string]string{"AEGIS_DEPLOY_RUN_AS": "specified_account", "AEGIS_DEPLOY_DETECTION_SCOPE": "user", "AEGIS_DEPLOY_SECRETS_FILE": path}
	spec := &pb.SoftwareDeploySpec{Parameters: map[string]string{"REGION": "apac"}}
	kind, err := applyPrivateDeployment(spec, func(key string) string { return env[key] })
	if err != nil || kind != "software_deploy_v2" {
		t.Fatalf("encrypted input refused: %v", err)
	}
	defer clear(spec.Execution.PasswordUtf8)
	defer clear(spec.SecureParameters)
	if spec.Execution.Username != fixture.Username || !bytes.Equal(spec.Execution.PasswordUtf8, fixture.Password) || spec.SecureParameters["API_TOKEN"] != fixture.Parameters["API_TOKEN"] {
		t.Fatal("private fields lost")
	}
	env["AEGIS_DEPLOY_RUN_AS"] = "system"
	invalid := &pb.SoftwareDeploySpec{}
	if _, err = applyPrivateDeployment(invalid, func(key string) string { return env[key] }); err == nil {
		t.Fatal("account credentials accepted on SYSTEM task")
	}
	for _, b := range invalid.Execution.PasswordUtf8 {
		if b != 0 {
			t.Fatal("rejected password buffer retained")
		}
	}
}
