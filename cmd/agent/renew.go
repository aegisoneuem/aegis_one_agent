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
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// certRenewalDue reports whether the client cert at certPath is past ~2/3 of its
// lifetime (time to renew) and its expiry. false if it can't be read/parsed.
func certRenewalDue(certPath string) (bool, time.Time) {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return false, time.Time{}
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return false, time.Time{}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false, time.Time{}
	}
	life := cert.NotAfter.Sub(cert.NotBefore)
	renewAt := cert.NotBefore.Add(life * 2 / 3) // renew once past 2/3 of lifetime
	return time.Now().After(renewAt), cert.NotAfter
}

// renewIfDue renews the client cert over the existing mTLS channel (authenticated
// by the current cert — no token) when it nears expiry, writing the new cert+key.
// Returns true if a renewal happened (caller should reconnect to use it).
func renewIfDue(ctx context.Context, client pb.AgentControlPlaneClient, cfg config.Config) (bool, error) {
	due, notAfter := certRenewalDue(cfg.TLSCertFile)
	if !due {
		return false, nil
	}
	slog.Info("cert renewal: due — requesting a new cert", "device_id", cfg.DeviceID, "expires", notAfter.Format(time.RFC3339))

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return false, err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: cfg.DeviceID}}, key)
	if err != nil {
		return false, err
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := client.Renew(cctx, &pb.RenewRequest{CsrPem: string(csrPEM)})
	if err != nil {
		return false, fmt.Errorf("renew RPC failed: %w", err)
	}
	if resp.Error != "" {
		return false, fmt.Errorf("renew rejected by server: %s", resp.Error)
	}
	if resp.ClientCertPem == "" {
		return false, fmt.Errorf("renew returned no certificate")
	}

	// Write the new key + cert. Remove the old sealed key so protect-at-rest
	// re-seals the new one on the next load (reconnect).
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return false, fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	_ = os.Remove(cfg.TLSKeyFile + ".enc")
	if err := os.WriteFile(cfg.TLSKeyFile, keyPEM, 0o600); err != nil {
		return false, fmt.Errorf("write client key: %w", err)
	}
	if err := os.WriteFile(cfg.TLSCertFile, []byte(resp.ClientCertPem), 0o644); err != nil {
		return false, fmt.Errorf("write client cert: %w", err)
	}
	slog.Info("cert renewal: success — new cert written; reconnecting to use it")
	return true, nil
}
