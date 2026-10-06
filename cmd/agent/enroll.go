package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/grpc"
)

// hasClientCert reports whether a usable client cert + key is already provisioned
// (either the plaintext key or the encrypted-at-rest copy).
func hasClientCert(cfg config.Config) bool {
	if _, err := os.Stat(cfg.TLSCertFile); err != nil {
		return false
	}
	if _, err := os.Stat(cfg.TLSKeyFile); err == nil {
		return true
	}
	if _, err := os.Stat(cfg.TLSKeyFile + ".enc"); err == nil {
		return true
	}
	return false
}

// ensureEnrolled makes sure the agent has a client certificate before it tries
// mTLS. If one is already provisioned it is a no-op. Otherwise it generates a
// keypair + CSR locally (private key never leaves the device), calls the server's
// Enroll RPC over SERVER-AUTH TLS authenticated by the bootstrap token, and writes
// the signed cert + key so normal mTLS can proceed. The at-rest protection then
// seals the key on the next load (see secure.ClientCredentials).
func ensureEnrolled(ctx context.Context, cfg config.Config, deviceID, systemUUID, machineGUID string) error {
	if hasClientCert(cfg) {
		slog.Info("enrollment: client cert already present, skipping")
		return nil
	}
	if cfg.EnrollAddress == "" || cfg.EnrollToken == "" {
		return fmt.Errorf("no client cert and enrollment not configured (set AEGIS_ENROLL_ADDRESS + AEGIS_ENROLL_TOKEN)")
	}
	slog.Info("enrollment: no client cert — enrolling", "enroll_address", cfg.EnrollAddress, "device_id", deviceID)

	// 1. Generate keypair + CSR (CN = device_id). The private key stays on-device.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: deviceID}}, key)
	if err != nil {
		return fmt.Errorf("create CSR: %w", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	// 2. Connect to the enrollment endpoint (server-auth TLS, no client cert) and
	//    present the bootstrap token.
	creds, err := secure.ClientTLSServerAuth(cfg.TLSCAFile, cfg.TLSServerName)
	if err != nil {
		return fmt.Errorf("enroll TLS setup: %w", err)
	}
	conn, err := grpc.NewClient(cfg.EnrollAddress, grpc.WithTransportCredentials(creds))
	if err != nil {
		return fmt.Errorf("dial enroll endpoint: %w", err)
	}
	defer conn.Close()

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := pb.NewAgentControlPlaneClient(conn).Enroll(cctx, &pb.EnrollRequest{
		EnrollToken:  cfg.EnrollToken,
		DeviceId:     deviceID,
		CsrPem:       string(csrPEM),
		SystemUuid:   systemUUID,
		MachineGuid:  machineGUID,
		Hostname:     cfg.Hostname,
		Os:           cfg.OS,
		AgentVersion: cfg.AgentVersion,
	})
	if err != nil {
		return fmt.Errorf("enroll RPC failed: %w", err)
	}
	if resp.Error != "" {
		return fmt.Errorf("enrollment rejected by server: %s", resp.Error)
	}
	if resp.ClientCertPem == "" {
		return fmt.Errorf("enrollment returned no certificate")
	}

	// 3. Persist the signed cert + private key. protect-at-rest will seal the key.
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.MkdirAll(filepath.Dir(cfg.TLSKeyFile), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(cfg.TLSKeyFile, keyPEM, 0o600); err != nil {
		return fmt.Errorf("write client key: %w", err)
	}
	if err := os.WriteFile(cfg.TLSCertFile, []byte(resp.ClientCertPem), 0o644); err != nil {
		return fmt.Errorf("write client cert: %w", err)
	}
	// Only write the CA if one isn't already provisioned (don't clobber the embedded one).
	if resp.CaChainPem != "" {
		if _, serr := os.Stat(cfg.TLSCAFile); serr != nil {
			_ = os.WriteFile(cfg.TLSCAFile, []byte(resp.CaChainPem), 0o644)
		}
	}
	slog.Info("enrollment: success — client cert provisioned",
		"device_id", deviceID, "assigned_device_id", resp.AssignedDeviceId, "cert", cfg.TLSCertFile)
	return nil
}
