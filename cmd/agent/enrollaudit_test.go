package main

import (
	"testing"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
)

// Enrollment + cert-renew events are written to the LOCAL trace (no server
// outbox) and are readable back via the same ReadAll the L3 audit_query uses.
// They are also EXEMPT from flood control, so a tight guard never drops them.
func TestEnrollAudit_LocalOnlyWrittenAndReadable(t *testing.T) {
	dir := t.TempDir()
	l, err := auditlog.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	savedL, savedG := auditLogger, auditGuard
	t.Cleanup(func() { auditLogger, auditGuard = savedL, savedG })
	auditLogger = l
	auditGuard = auditlog.NewGuard(1, 1, time.Second) // deliberately tiny budget

	// nil outbox (pre-connection) must neither block nor panic.
	recordLocalAuditEvent(enrollEvent("dev-1", "requested", "enrolling via host:50051"))
	recordLocalAuditEvent(enrollEvent("dev-1", "succeeded", "client cert provisioned"))
	recordLocalAuditEvent(certRenewEvent("dev-1", "succeeded", "new client cert written"))
	// Even many enrollment events in a burst are never suppressed (exempt).
	for i := 0; i < 20; i++ {
		recordLocalAuditEvent(enrollEvent("dev-1", "failed", "transient"))
	}

	evs, err := auditlog.ReadAll(dir, false)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var enroll, renew int
	for _, e := range evs {
		switch e.EventType {
		case "enrollment":
			enroll++
		case "cert_renew":
			renew++
		}
	}
	if enroll != 22 { // requested + succeeded + 20 failed, none suppressed
		t.Fatalf("expected 22 enrollment events (none suppressed), got %d", enroll)
	}
	if renew != 1 {
		t.Fatalf("expected 1 cert_renew event, got %d", renew)
	}

	// And they are queryable exactly like command events (L3 reuse).
	got := auditlog.Filter(evs, auditlog.Query{Mode: "search", Pattern: "cert provisioned"})
	if len(got) != 1 || got[0].Decision != "succeeded" {
		t.Fatalf("enrollment event not queryable: %+v", got)
	}
}

// recordLocalAuditEvent must be a no-op-safe path when the local logger is absent
// (startup failure) and the outbox is nil — it must not panic or block.
func TestEnrollAudit_NoLoggerNoOutboxIsSafe(t *testing.T) {
	savedL, savedG := auditLogger, auditGuard
	t.Cleanup(func() { auditLogger, auditGuard = savedL, savedG })
	auditLogger = nil
	auditGuard = nil
	recordLocalAuditEvent(enrollEvent("dev-x", "requested", "no logger"))
	// reaching here without panic/deadlock is the assertion
}
