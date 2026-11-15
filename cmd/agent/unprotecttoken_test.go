package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func withTokenVerifyKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	orig := cmdVerifyKey
	cmdVerifyKey = pub
	t.Cleanup(func() { cmdVerifyKey = orig })
	return priv
}

func TestUnprotectToken_StaticValid(t *testing.T) {
	priv := withTokenVerifyKey(t)
	tok := mintUnprotectToken(priv, unprotectPayload{Device: "*", Exp: 0, Nonce: "n1"})
	if ok, reason := verifyUnprotectToken(tok, "any-device"); !ok {
		t.Fatalf("static token must verify, got: %s", reason)
	}
}

func TestUnprotectToken_PerDeviceValid(t *testing.T) {
	priv := withTokenVerifyKey(t)
	tok := mintUnprotectToken(priv, unprotectPayload{Device: "dev-123", Exp: time.Now().Add(time.Hour).Unix(), Nonce: "n2"})
	if ok, reason := verifyUnprotectToken(tok, "DEV-123"); !ok { // case-insensitive
		t.Fatalf("per-device token must verify, got: %s", reason)
	}
}

func TestUnprotectToken_WrongDevice(t *testing.T) {
	priv := withTokenVerifyKey(t)
	tok := mintUnprotectToken(priv, unprotectPayload{Device: "dev-123", Exp: 0, Nonce: "n3"})
	if ok, _ := verifyUnprotectToken(tok, "other-device"); ok {
		t.Fatal("token for a different device must be rejected")
	}
}

func TestUnprotectToken_Expired(t *testing.T) {
	priv := withTokenVerifyKey(t)
	tok := mintUnprotectToken(priv, unprotectPayload{Device: "*", Exp: time.Now().Add(-time.Minute).Unix(), Nonce: "n4"})
	if ok, _ := verifyUnprotectToken(tok, "x"); ok {
		t.Fatal("expired token must be rejected")
	}
}

func TestUnprotectToken_BadSignature(t *testing.T) {
	withTokenVerifyKey(t)
	// Token minted with a DIFFERENT key.
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	tok := mintUnprotectToken(otherPriv, unprotectPayload{Device: "*", Exp: 0, Nonce: "n5"})
	if ok, _ := verifyUnprotectToken(tok, "x"); ok {
		t.Fatal("token signed by the wrong key must be rejected")
	}
}

func TestUnprotectToken_Malformed(t *testing.T) {
	withTokenVerifyKey(t)
	for _, bad := range []string{"", "nodot", "a.b.c", "!!!.???"} {
		if ok, _ := verifyUnprotectToken(bad, "x"); ok {
			t.Fatalf("malformed token %q must be rejected", bad)
		}
	}
}

func TestUnprotectToken_NoKeyLoaded(t *testing.T) {
	orig := cmdVerifyKey
	cmdVerifyKey = nil
	defer func() { cmdVerifyKey = orig }()
	if ok, _ := verifyUnprotectToken("whatever.sig", "x"); ok {
		t.Fatal("with no verify key loaded, nothing must verify")
	}
}
