package main

import (
	"context"
	"crypto/ed25519"
	"crypto/fips140"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// securityPosture is the agent's self-reported security configuration — the
// "prove it" surface for a customer (or auditor) asking what protections are in
// force, the way BigFix declares its posture in the masthead + client logs.
// Every field reflects what the agent ACTUALLY enforces; nothing is claimed that
// the code does not do (e.g. FIPS reflects the live Go FIPS-140 mode, not a
// marketing flag).
type securityPosture struct {
	TransportTLS     string // transport protection (mTLS + min TLS version)
	CommandAuthz     string // how server commands are authorized (Ed25519 signature)
	CommandAuthzOK   bool   // true only with a non-dev key loaded
	AtRest           string // at-rest protection summary
	AuditTrail       string // audit coverage summary
	FIPSMode         bool   // live Go FIPS 140-3 module state (crypto/fips140.Enabled)
	FIPSRequired     bool   // AEGIS_REQUIRE_FIPS — agent refuses to run without FIPS
	ClientCert       string // client cert serial + expiry summary
	AgentVersion     string
}

// collectSecurityPosture snapshots the live security configuration. It reads the
// already-resolved command-verify-key state and the client cert on disk.
func collectSecurityPosture(cfg config.Config) securityPosture {
	p := securityPosture{
		TransportTLS: "mTLS (mutual), min TLS 1.3",
		AtRest:       "DPAPI-sealed device identity + client key; AES-GCM encrypted content cache",
		AuditTrail:   auditTrailSummary(),
		FIPSMode:     fips140.Enabled(),
		FIPSRequired: cfg.RequireFIPS,
		AgentVersion: cfg.AgentVersion,
		ClientCert:   clientCertSummary(cfg.TLSCertFile),
	}
	switch {
	case len(cmdVerifyKey) != ed25519.PublicKeySize:
		p.CommandAuthz = "NONE — no verify key loaded (commands cannot be trusted)"
	case cmdVerifyKeyIsDev:
		p.CommandAuthz = "Ed25519 signature — BUILT-IN DEV KEY (set AEGIS_CMD_VERIFY_KEY for production)"
	default:
		p.CommandAuthz = "Ed25519 signature (" + cmdVerifyKeySource + ")"
		p.CommandAuthzOK = true
	}
	return p
}

// auditTrailSummary describes the live audit coverage.
func auditTrailSummary() string {
	parts := []string{"server stream (authoritative)"}
	if auditLogger != nil {
		parts = append(parts, "local JSONL trace")
	}
	if auditGuard != nil {
		parts = append(parts, "flood-resistant")
	}
	return strings.Join(parts, " + ")
}

// clientCertSummary reads the client cert and returns a short serial/expiry line.
func clientCertSummary(certPath string) string {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return "not provisioned"
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "unreadable"
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "unparseable"
	}
	days := int(time.Until(cert.NotAfter).Hours() / 24)
	return fmt.Sprintf("serial=%s, expires=%s (%dd left)", cert.SerialNumber.Text(16), cert.NotAfter.UTC().Format("2006-01-02"), days)
}

// details renders the posture as a compact key=value string for the audit event.
func (p securityPosture) details() string {
	return fmt.Sprintf("tls=%q; command_authz=%q; at_rest=%q; audit=%q; fips_mode=%t; fips_required=%t; client_cert=%q; agent_version=%q",
		p.TransportTLS, p.CommandAuthz, p.AtRest, p.AuditTrail, p.FIPSMode, p.FIPSRequired, p.ClientCert, p.AgentVersion)
}

// enforceFIPS implements fail-closed FIPS: when AEGIS_REQUIRE_FIPS is set, the
// agent MUST be running with the Go FIPS 140-3 module active (built with
// GOFIPS140 and run with GODEBUG=fips140=on/only). If it is not, enforceFIPS
// returns an error so startup aborts — a compliance customer gets no agent at
// all rather than a non-FIPS one. When FIPS is not required this is a no-op.
func enforceFIPS(cfg config.Config) error {
	if !cfg.RequireFIPS {
		return nil
	}
	if !fips140.Enabled() {
		return fmt.Errorf("AEGIS_REQUIRE_FIPS is set but the FIPS 140-3 module is NOT active — build with GOFIPS140=latest and run with GODEBUG=fips140=on; refusing to start")
	}
	slog.Info("FIPS 140-3 mode is ACTIVE and required — proceeding")
	return nil
}

// reportSecurityPosture logs the posture as a clear, greppable startup callout
// (BigFix-style) AND emits a signed-stream security_posture audit event so the
// console shows a per-device attestation and an operator can pull it remotely via
// the L3 audit_query. Called once at startup after the audit logger and verify
// key are initialized.
func reportSecurityPosture(cfg config.Config, outbox chan<- *pb.AgentMessage, deviceID string) {
	p := collectSecurityPosture(cfg)

	slog.Info("SECURITY POSTURE — transport", "value", p.TransportTLS)
	slog.Info("SECURITY POSTURE — command authorization", "value", p.CommandAuthz)
	slog.Info("SECURITY POSTURE — data at rest", "value", p.AtRest)
	slog.Info("SECURITY POSTURE — audit trail", "value", p.AuditTrail)
	slog.Info("SECURITY POSTURE — FIPS 140-3 mode", "enabled", p.FIPSMode, "required", p.FIPSRequired)
	slog.Info("SECURITY POSTURE — client certificate", "value", p.ClientCert)
	if !p.CommandAuthzOK {
		slog.Warn("SECURITY POSTURE — command authorization is NOT production-ready", "reason", p.CommandAuthz)
	}

	recordAuditEvent(context.Background(), outbox, auditlog.Event{
		DeviceID:  deviceID,
		EventType: "security_posture",
		Decision:  "reported",
		Reason:    "agent security configuration at startup",
		Details:   p.details(),
	})
}
