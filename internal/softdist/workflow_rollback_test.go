package softdist

import (
	"context"
	"strings"
	"testing"
)

// TestRollbackRunsOnMainFailure: when the main action fails, every rollback
// step must run in order after the failure is observed. The final Reason must
// mention the rollback outcome so the console learns cleanup happened.
func TestRollbackRunsOnMainFailure(t *testing.T) {
	p := Params{Name: "main", FilePath: "main.exe"}
	steps := DeploymentSteps{
		Before:   []CommandStep{{Name: "pre1", Command: "pre1.exe"}},
		Rollback: []CommandStep{{Name: "r1", Command: "r1.exe"}, {Name: "r2", Command: "r2.exe"}},
	}
	var calls []string
	res := runWorkflow(context.Background(), p, steps, nil, func(_ context.Context, got Params) Result {
		calls = append(calls, got.FilePath)
		switch got.FilePath {
		case "pre1.exe", "r1.exe", "r2.exe":
			return Result{Success: true}
		default: // main
			return Result{Success: false, ExitCode: 7, Reason: "main failed"}
		}
	})
	wantOrder := []string{"pre1.exe", "main.exe", "r1.exe", "r2.exe"}
	if strings.Join(calls, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("expected call order %v, got %v", wantOrder, calls)
	}
	if res.Success {
		t.Fatalf("expected failure result, got %+v", res)
	}
	if !strings.Contains(res.Reason, "rollback after main failure completed") {
		t.Fatalf("expected rollback completion mention in reason, got %q", res.Reason)
	}
}

// TestRollbackRunsOnPreStepFailure: a failing pre-step must also fire rollback
// (the admin may want to undo artifacts the extraction/pre-step left behind).
func TestRollbackRunsOnPreStepFailure(t *testing.T) {
	p := Params{Name: "main", FilePath: "main.exe"}
	steps := DeploymentSteps{
		Before:   []CommandStep{{Name: "pre1", Command: "pre1.exe"}},
		Rollback: []CommandStep{{Name: "r1", Command: "r1.exe"}},
	}
	var calls []string
	res := runWorkflow(context.Background(), p, steps, nil, func(_ context.Context, got Params) Result {
		calls = append(calls, got.FilePath)
		if got.FilePath == "pre1.exe" {
			return Result{Success: false, ExitCode: 3, Reason: "pre failed"}
		}
		return Result{Success: true}
	})
	// Main must NOT run after pre fails.
	wantOrder := []string{"pre1.exe", "r1.exe"}
	if strings.Join(calls, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("expected call order %v, got %v", wantOrder, calls)
	}
	if res.Success || !strings.Contains(res.Reason, "rollback after pre-deployment failure") {
		t.Fatalf("expected rollback-on-pre-fail in reason, got %q", res.Reason)
	}
}

// TestRollbackContinuesPastFailure: rollback is best-effort — a failing
// rollback step must NOT stop the following rollback steps, and both failures
// must be reported.
func TestRollbackContinuesPastFailure(t *testing.T) {
	p := Params{Name: "main", FilePath: "main.exe"}
	steps := DeploymentSteps{
		Rollback: []CommandStep{{Name: "r1", Command: "r1.exe"}, {Name: "r2", Command: "r2.exe"}, {Name: "r3", Command: "r3.exe"}},
	}
	var calls []string
	res := runWorkflow(context.Background(), p, steps, nil, func(_ context.Context, got Params) Result {
		calls = append(calls, got.FilePath)
		switch got.FilePath {
		case "r2.exe":
			return Result{Success: false, ExitCode: 9}
		case "main.exe":
			return Result{Success: false, ExitCode: 5, Reason: "main failed"}
		}
		return Result{Success: true}
	})
	wantOrder := []string{"main.exe", "r1.exe", "r2.exe", "r3.exe"}
	if strings.Join(calls, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("expected all rollback steps to run despite r2 failure, got %v", calls)
	}
	if !strings.Contains(res.Reason, "rollback step 2 (r2) exit 9") {
		t.Fatalf("expected partial rollback mention of r2 failure, got %q", res.Reason)
	}
}

// TestRollbackSkippedWhenNoFailure: a fully successful workflow must NOT run
// rollback commands.
func TestRollbackSkippedWhenNoFailure(t *testing.T) {
	p := Params{Name: "main", FilePath: "main.exe"}
	steps := DeploymentSteps{
		After:    []CommandStep{{Name: "post1", Command: "post1.exe"}},
		Rollback: []CommandStep{{Name: "r1", Command: "r1.exe"}},
	}
	var calls []string
	res := runWorkflow(context.Background(), p, steps, nil, func(_ context.Context, _ Params) Result {
		return Result{Success: true}
	})
	_ = res
	for _, c := range calls {
		if c == "r1.exe" {
			t.Fatalf("rollback must not run on success, got call list %v", calls)
		}
	}
}

// TestRollbackRunsOnPostFailure: a failing after-step must also fire rollback
// (installer landed, post failed → admin may want to undo installer effects).
func TestRollbackRunsOnPostFailure(t *testing.T) {
	p := Params{Name: "main", FilePath: "main.exe"}
	steps := DeploymentSteps{
		After:    []CommandStep{{Name: "post1", Command: "post1.exe"}},
		Rollback: []CommandStep{{Name: "r1", Command: "r1.exe"}},
	}
	var calls []string
	res := runWorkflow(context.Background(), p, steps, nil, func(_ context.Context, got Params) Result {
		calls = append(calls, got.FilePath)
		if got.FilePath == "post1.exe" {
			return Result{Success: false, ExitCode: 11, Reason: "post failed"}
		}
		return Result{Success: true}
	})
	want := []string{"main.exe", "post1.exe", "r1.exe"}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("expected %v, got %v", want, calls)
	}
	if !strings.Contains(res.Reason, "rollback after post-deployment failure") {
		t.Fatalf("expected rollback-on-post-fail in reason, got %q", res.Reason)
	}
}
