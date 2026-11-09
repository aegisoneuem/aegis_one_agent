package main

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

func withVerifyKey(t *testing.T, key ed25519.PublicKey, source string, isDev bool) {
	t.Helper()
	sk, ss, sd := cmdVerifyKey, cmdVerifyKeySource, cmdVerifyKeyIsDev
	t.Cleanup(func() { cmdVerifyKey, cmdVerifyKeySource, cmdVerifyKeyIsDev = sk, ss, sd })
	cmdVerifyKey, cmdVerifyKeySource, cmdVerifyKeyIsDev = key, source, isDev
}

func goodKey() ed25519.PublicKey {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	return pub
}

func TestPosture_CommandAuthzBranches(t *testing.T) {
	// No key loaded.
	withVerifyKey(t, nil, "unloaded", false)
	if p := collectSecurityPosture(config.Config{}); p.CommandAuthzOK || !strings.Contains(p.CommandAuthz, "NONE") {
		t.Fatalf("no-key: %+v", p.CommandAuthz)
	}
	// Dev key.
	withVerifyKey(t, goodKey(), "built-in dev key", true)
	if p := collectSecurityPosture(config.Config{}); p.CommandAuthzOK || !strings.Contains(p.CommandAuthz, "DEV KEY") {
		t.Fatalf("dev-key: %+v", p.CommandAuthz)
	}
	// Production key.
	withVerifyKey(t, goodKey(), "config (AEGIS_CMD_VERIFY_KEY)", false)
	p := collectSecurityPosture(config.Config{})
	if !p.CommandAuthzOK || !strings.Contains(p.CommandAuthz, "Ed25519") {
		t.Fatalf("prod-key: %+v", p.CommandAuthz)
	}
}

func TestPosture_DetailsAndStaticFields(t *testing.T) {
	withVerifyKey(t, goodKey(), "config", false)
	p := collectSecurityPosture(config.Config{AgentVersion: "9.9.9"})
	if !strings.Contains(p.TransportTLS, "TLS 1.3") || !strings.Contains(p.TransportTLS, "mTLS") {
		t.Fatalf("transport: %q", p.TransportTLS)
	}
	d := p.details()
	for _, want := range []string{"tls=", "command_authz=", "at_rest=", "audit=", "fips_mode=", "fips_required=", "client_cert=", `agent_version="9.9.9"`} {
		if !strings.Contains(d, want) {
			t.Fatalf("details missing %q: %s", want, d)
		}
	}
}

// Fail-closed FIPS: required-but-inactive aborts startup; not-required is a no-op.
// (The normal test binary is not a FIPS build, so fips140.Enabled() is false.)
func TestEnforceFIPS(t *testing.T) {
	if err := enforceFIPS(config.Config{RequireFIPS: false}); err != nil {
		t.Fatalf("not-required must be a no-op, got %v", err)
	}
	err := enforceFIPS(config.Config{RequireFIPS: true})
	if err == nil {
		t.Fatal("required-but-inactive must return an error (fail-closed)")
	}
	if !strings.Contains(err.Error(), "FIPS") || !strings.Contains(err.Error(), "refusing to start") {
		t.Fatalf("error should explain the FIPS refusal: %v", err)
	}
}

func TestPosture_ClientCertSummary(t *testing.T) {
	if got := clientCertSummary(filepath.Join(t.TempDir(), "none.crt")); got != "not provisioned" {
		t.Fatalf("missing cert: %q", got)
	}
	// A real self-signed cert → serial + expiry are summarized.
	dir := t.TempDir()
	certPath := filepath.Join(dir, "client.crt")
	writeSelfSignedCert(t, certPath)
	got := clientCertSummary(certPath)
	if !strings.Contains(got, "serial=") || !strings.Contains(got, "expires=") || !strings.Contains(got, "left") {
		t.Fatalf("cert summary: %q", got)
	}
}

// reportSecurityPosture emits a security_posture audit event through the REAL
// audit path, and it is exempt from flood control (never suppressed).
func TestPosture_EmitsAttestationEvent(t *testing.T) {
	savedG := auditGuard
	t.Cleanup(func() { auditGuard = savedG })
	auditGuard = auditlog.NewGuard(1, 1, time.Second) // tight; must not suppress posture
	withVerifyKey(t, goodKey(), "config (AEGIS_CMD_VERIFY_KEY)", false)

	outbox := make(chan *pb.AgentMessage, 8)
	reportSecurityPosture(config.Config{DeviceID: "dev-1", AgentVersion: "1.2.3"}, outbox, "dev-1")
	close(outbox)

	var found *pb.AuditEvent
	for m := range outbox {
		if ae := m.GetAuditEvent(); ae != nil && ae.EventType == "security_posture" {
			found = ae
		}
	}
	if found == nil {
		t.Fatal("no security_posture audit event emitted")
	}
	if !strings.Contains(found.Details, "command_authz=") || !strings.Contains(found.Details, "fips_mode=") {
		t.Fatalf("attestation details incomplete: %s", found.Details)
	}
	if found.DeviceId != "dev-1" || found.Decision != "reported" {
		t.Fatalf("attestation fields: %+v", found)
	}
}

func writeSelfSignedCert(t *testing.T, path string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(0x1234abcd),
		Subject:      pkix.Name{CommonName: "test-device"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o644); err != nil {
		t.Fatal(err)
	}
}
