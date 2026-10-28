//go:build windows

package softdist

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// TestJobGroupKillsDescendantsOnClose: cmd.exe spawns a long-running ping
// child. Closing the job object must kill BOTH the cmd.exe (ping-wait finishes
// naturally when its child dies) and the ping grandchild — proving descendants
// cannot survive the agent closing the installer wrapper. This is the gap the
// Firefox/MSI acceptance notes flagged as "descendants can survive timeout".
func TestJobGroupKillsDescendantsOnClose(t *testing.T) {
	job, err := newJobGroup("test")
	if err != nil {
		t.Fatalf("newJobGroup: %v", err)
	}
	defer job.Close()

	// cmd that spawns ping for ~30s. The child should die when we close the job
	// well before 30s. We measure elapsed time to prove kill, not natural exit.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "cmd.exe", "/c", "ping", "-n", "30", "127.0.0.1")

	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start cmd: %v", err)
	}
	if err := job.Attach(cmd); err != nil {
		t.Fatalf("attach: %v", err)
	}

	// Give the child a brief head start, then TerminateJobObject should kill
	// everything (including the ping grandchild).
	time.Sleep(200 * time.Millisecond)
	if err := job.Terminate(1); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	_ = cmd.Wait()
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("expected job terminate to end descendants in <5s, took %s", elapsed)
	}
}

// TestJobGroupActiveDescendants: with a running child in the job, the count
// must be at least 1. After the job terminates, the count drops back to 0.
// This is the diagnostic signal used by describeDiagnostics() to tell a stuck
// parent apart from a stuck-child timeout.
func TestJobGroupActiveDescendants(t *testing.T) {
	job, err := newJobGroup("count")
	if err != nil {
		t.Fatalf("newJobGroup: %v", err)
	}
	defer job.Close()
	cmd := exec.Command("cmd.exe", "/c", "ping", "-n", "10", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start cmd: %v", err)
	}
	if err := job.Attach(cmd); err != nil {
		t.Fatalf("attach: %v", err)
	}
	// Give cmd.exe a brief moment to spawn its ping child.
	time.Sleep(300 * time.Millisecond)
	if got := job.activeDescendants(); got < 1 {
		t.Fatalf("expected at least 1 active descendant, got %d", got)
	}
	_ = job.Terminate(1)
	_ = cmd.Wait()
	// After terminate, descendants are gone.
	if got := job.activeDescendants(); got != 0 {
		t.Fatalf("expected 0 active after terminate, got %d", got)
	}
}

// TestJobGroupCloseIdempotent: calling Close twice must not error.
func TestJobGroupCloseIdempotent(t *testing.T) {
	job, err := newJobGroup("x")
	if err != nil {
		t.Fatalf("newJobGroup: %v", err)
	}
	if err := job.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := job.Close(); err != nil {
		t.Fatalf("second Close should be no-op: %v", err)
	}
}
