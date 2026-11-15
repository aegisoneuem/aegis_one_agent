package main

import (
	"os"
	"path/filepath"
	"testing"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// harness swaps the baseline store + exe path + alert sink for in-memory fakes.
func withBinaryHarness(t *testing.T, exeContent string) (setContent func(string), alerts *[]string) {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "agent.exe")
	if err := os.WriteFile(exe, []byte(exeContent), 0o644); err != nil {
		t.Fatal(err)
	}
	var baseVer, baseHash string
	captured := []string{}

	origRead, origWrite, origAlert, origExe := readBinaryBaseline, writeBinaryBaseline, binaryAlert, osExecutable
	readBinaryBaseline = func() (string, string) { return baseVer, baseHash }
	writeBinaryBaseline = func(v, h string) error { baseVer, baseHash = v, h; return nil }
	binaryAlert = func(_ chan<- *pb.AgentMessage, _, kind, _ string) { captured = append(captured, kind) }
	osExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() {
		readBinaryBaseline, writeBinaryBaseline, binaryAlert, osExecutable = origRead, origWrite, origAlert, origExe
	})

	return func(c string) {
		if err := os.WriteFile(exe, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}, &captured
}

func TestBinaryIntegrity_FirstRunRecordsNoAlert(t *testing.T) {
	_, alerts := withBinaryHarness(t, "v1-bytes")
	checkBinaryIntegrity(nil, "dev", "1.0.0")
	if len(*alerts) != 0 {
		t.Fatalf("first run must not alert, got %v", *alerts)
	}
}

func TestBinaryIntegrity_SameVersionChangedHashAlerts(t *testing.T) {
	setContent, alerts := withBinaryHarness(t, "v1-bytes")
	checkBinaryIntegrity(nil, "dev", "1.0.0") // baseline
	setContent("v1-TAMPERED")                 // same version, different bytes
	checkBinaryIntegrity(nil, "dev", "1.0.0")
	if len(*alerts) != 1 || (*alerts)[0] != "modified" {
		t.Fatalf("expected one 'modified' alert, got %v", *alerts)
	}
	// Re-armed: a third run with the SAME tampered bytes must not re-alert.
	checkBinaryIntegrity(nil, "dev", "1.0.0")
	if len(*alerts) != 1 {
		t.Fatalf("tripwire must re-arm (alert once), got %v", *alerts)
	}
}

func TestBinaryIntegrity_VersionBumpIsSilent(t *testing.T) {
	setContent, alerts := withBinaryHarness(t, "v1-bytes")
	checkBinaryIntegrity(nil, "dev", "1.0.0") // baseline
	setContent("v2-bytes")                    // new bytes...
	checkBinaryIntegrity(nil, "dev", "1.1.0") // ...with a new version = legit update
	if len(*alerts) != 0 {
		t.Fatalf("a version bump must be silent, got %v", *alerts)
	}
}

func TestBinaryIntegrity_UnchangedIsSilent(t *testing.T) {
	_, alerts := withBinaryHarness(t, "stable")
	checkBinaryIntegrity(nil, "dev", "1.0.0")
	checkBinaryIntegrity(nil, "dev", "1.0.0") // same version, same bytes
	if len(*alerts) != 0 {
		t.Fatalf("unchanged binary must not alert, got %v", *alerts)
	}
}
