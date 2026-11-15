package main

import (
	"testing"
	"time"
)

func setTestPasscode(t *testing.T, code string) {
	t.Helper()
	salt := []byte("0123456789abcdef")
	h := pbkdf2SHA256([]byte(code), salt, 1000, passcodeKeyLen) // low iter = fast test
	passcodeMu.Lock()
	passcodeState = &storedPasscode{Mode: "static", Salt: salt, Iter: 1000, Hash: h}
	passcodeMu.Unlock()
	resetRateLimit()
	t.Cleanup(func() {
		passcodeMu.Lock()
		passcodeState = nil
		passcodeMu.Unlock()
		resetRateLimit()
	})
}

func resetRateLimit() {
	rlMu.Lock()
	rlFails = 0
	rlUntil = time.Time{}
	rlMu.Unlock()
}

func TestPasscode_VerifyCorrectAndWrong(t *testing.T) {
	setTestPasscode(t, "Secret99")
	if !verifyPasscode("Secret99") {
		t.Fatal("correct passcode must verify")
	}
	if verifyPasscode("wrongpass") {
		t.Fatal("wrong passcode must fail")
	}
	if !passcodeConfigured() {
		t.Fatal("passcode should be configured")
	}
}

func TestPasscode_AuthorizeWithPasscode(t *testing.T) {
	setTestPasscode(t, "Open1234")
	if ok, reason := authorizeUnprotect("Open1234", "dev-1"); !ok {
		t.Fatalf("authorize with correct passcode failed: %s", reason)
	}
	if ok, _ := authorizeUnprotect("nope", "dev-1"); ok {
		t.Fatal("authorize with wrong passcode must fail")
	}
}

func TestPasscode_LockoutAfterRepeatedFailures(t *testing.T) {
	setTestPasscode(t, "RightCode")
	for i := 0; i < rlMaxFails; i++ {
		if ok, _ := authorizeUnprotect("badcode", "dev"); ok {
			t.Fatal("bad code must not authorize")
		}
	}
	// Now locked out — even the CORRECT code is rejected until the window passes.
	if ok, reason := authorizeUnprotect("RightCode", "dev"); ok {
		t.Fatal("must be locked out after repeated failures")
	} else if reason == "" {
		t.Fatal("lockout should give a reason")
	}
	if locked, _ := passcodeLockedOut(); !locked {
		t.Fatal("expected lockout state")
	}
}

func TestPasscode_NoneConfigured(t *testing.T) {
	passcodeMu.Lock()
	passcodeState = nil
	passcodeMu.Unlock()
	resetRateLimit()
	if passcodeConfigured() {
		t.Fatal("no passcode should be configured")
	}
	if verifyPasscode("anything") {
		t.Fatal("verify must be false with no passcode set")
	}
}
