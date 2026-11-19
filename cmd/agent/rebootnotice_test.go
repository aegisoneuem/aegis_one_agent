package main

import "testing"

func TestRebootNoticeRoundTripAndOnce(t *testing.T) {
	dir := t.TempDir()

	// No marker → nothing to consume.
	if _, ok := consumeRebootNotice(dir); ok {
		t.Fatal("unexpected marker present")
	}

	writeRebootNotice(dir, "This restart applies update KB5001234.")

	detail, ok := consumeRebootNotice(dir)
	if !ok || detail != "This restart applies update KB5001234." {
		t.Fatalf("consume = (%q, %v), want the written detail", detail, ok)
	}

	// Second consume must find nothing (shown exactly once).
	if _, ok := consumeRebootNotice(dir); ok {
		t.Fatal("marker should have been cleared after first consume")
	}
}
