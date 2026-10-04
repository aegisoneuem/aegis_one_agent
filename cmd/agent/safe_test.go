package main

import "testing"

func TestSafeRunRecoversPanic(t *testing.T) {
	panicked := safeRun("test", func() { panic("boom") })
	if !panicked {
		t.Fatal("expected panicked=true")
	}
	// Reaching here proves the panic was recovered (the process did not crash).
}

func TestSafeRunNormalReturn(t *testing.T) {
	ran := false
	panicked := safeRun("test", func() { ran = true })
	if panicked {
		t.Fatal("unexpected panic report")
	}
	if !ran {
		t.Fatal("fn did not run")
	}
}
