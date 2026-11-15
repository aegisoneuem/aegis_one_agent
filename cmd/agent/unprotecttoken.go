package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// Unprotect-token engine — authorizes a PROTECTED operation (stop / disable /
// uninstall) when tamper-protection is on. The server mints a token signed with
// the SAME Ed25519 key whose public half the agent already trusts for commands
// (cmdVerifyKey). The server team decides the policy — a STATIC shared token
// (exp=0, device="*") or a ROTATING per-device one (device set, exp set) — and the
// agent ACCEPTS WHATEVER VERIFIES. Verification is STATELESS: the token presented
// at the moment of the operation is checked; nothing is stored.
//
// Token wire form: base64url(payloadJSON) + "." + base64url(ed25519_signature).

type unprotectPayload struct {
	Device string `json:"device"` // device id the token is valid for, or "*" (shared)
	Exp    int64  `json:"exp"`    // unix seconds expiry; 0 = never (static token)
	Nonce  string `json:"nonce"`  // random, so two tokens with the same claims differ
}

// verifyUnprotectToken returns ok, plus a reason when it fails (for the audit
// trail). It checks signature, device scope, and expiry.
func verifyUnprotectToken(token, deviceID string) (bool, string) {
	if len(cmdVerifyKey) != ed25519.PublicKeySize {
		return false, "no command-verify key loaded"
	}
	parts := strings.SplitN(strings.TrimSpace(token), ".", 2)
	if len(parts) != 2 {
		return false, "malformed token"
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false, "bad token payload encoding"
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false, "bad token signature encoding"
	}
	if !ed25519.Verify(cmdVerifyKey, payloadBytes, sig) {
		return false, "token signature invalid"
	}
	var p unprotectPayload
	if err := json.Unmarshal(payloadBytes, &p); err != nil {
		return false, "bad token payload"
	}
	if p.Device != "*" && !strings.EqualFold(p.Device, deviceID) {
		return false, "token is not valid for this device"
	}
	if p.Exp != 0 && time.Now().Unix() > p.Exp {
		return false, "token expired"
	}
	return true, ""
}

// mintUnprotectToken signs a token (server/test side). The real server mints these
// with its private key; kept here so tests (and a dev helper) can produce one.
func mintUnprotectToken(priv ed25519.PrivateKey, p unprotectPayload) string {
	payloadBytes, _ := json.Marshal(p)
	sig := ed25519.Sign(priv, payloadBytes)
	return base64.RawURLEncoding.EncodeToString(payloadBytes) + "." + base64.RawURLEncoding.EncodeToString(sig)
}
