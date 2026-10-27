package softdist

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestWorkflowSecretsAndExecutionScope(t *testing.T) {
	session := &Session{}
	p := Params{Name: "main", FilePath: "main.exe", WorkingDirectory: "verified-root", Session: session,
		SecureParameters: map[string]string{"TOKEN": "private-value"}, Parameters: map[string]string{"REGION": "apac"},
		ProductCode: "main-product", ExpectedHash: "main-hash", SuccessCodes: []int32{42}}
	steps := DeploymentSteps{Before: []CommandStep{{Name: "before", Command: "pre.exe"}}, After: []CommandStep{{Name: "after", Command: "post.exe", UseSecureParameters: true}}}
	var calls []string
	res := runWorkflow(context.Background(), p, steps, nil, func(_ context.Context, got Params) Result {
		calls = append(calls, got.FilePath)
		if got.Session != session || got.WorkingDirectory != p.WorkingDirectory || got.Parameters["REGION"] != "apac" {
			t.Fatal("token/cwd/ordinary parameter scope changed between commands")
		}
		if got.FilePath == "pre.exe" {
			if len(got.SecureParameters) != 0 || !got.Sensitive || got.Timeout != 5*time.Minute {
				t.Fatal("pre command received unrequested secrets or lost output suppression/default timeout")
			}
		}
		if got.FilePath == "post.exe" && got.SecureParameters["TOKEN"] != "private-value" {
			t.Fatal("explicit private stdin opt-in was lost")
		}
		if got.FilePath != "main.exe" && (got.ProductCode != "" || got.ExpectedHash != "" || len(got.SuccessCodes) != 0) {
			t.Fatal("main installer settings leaked into steps")
		}
		return Result{Success: true}
	})
	if !res.Success || strings.Join(calls, ",") != "pre.exe,main.exe,post.exe" {
		t.Fatalf("unexpected order/result: %v %+v", calls, res)
	}
}

func TestWorkflowPublicSecretRejectedBeforeAnyCommand(t *testing.T) {
	for _, step := range []CommandStep{
		{Name: "private-value", Command: "cmd.exe"},
		{Name: "step", Command: "private-value"},
		{Name: "step", Command: "cmd.exe", Arguments: "${TOKEN}"},
	} {
		p := Params{SecureParameters: map[string]string{"TOKEN": "private-value"}}
		res := runWorkflow(context.Background(), p, DeploymentSteps{After: []CommandStep{step}}, nil, func(context.Context, Params) Result {
			t.Fatal("invalid public secret executed a command")
			return Result{}
		})
		if res.Success || !strings.Contains(res.Reason, "secure") {
			t.Fatalf("secret not refused: %+v", res)
		}
	}
}

func TestWorkflowRebootAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		code        int
		wantCalls   int
		wantSuccess bool
	}{{"pending reboot preserved", 3010, 3, true}, {"reboot initiated stops sequence", 1641, 1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			steps := DeploymentSteps{Before: []CommandStep{{Name: "before", Command: "pre"}}, After: []CommandStep{{Name: "after", Command: "post"}}}
			res := runWorkflow(context.Background(), Params{}, steps, nil, func(context.Context, Params) Result {
				calls++
				return Result{Success: true, ExitCode: tc.code, RebootRequired: true}
			})
			if calls != tc.wantCalls || res.Success != tc.wantSuccess || !res.RebootRequired {
				t.Fatalf("incorrect reboot handling: calls=%d result=%+v", calls, res)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := runWorkflow(ctx, Params{}, DeploymentSteps{After: []CommandStep{{Name: "post", Command: "post"}}}, nil, func(context.Context, Params) Result {
		t.Fatal("cancelled deployment started a command")
		return Result{}
	})
	if res.Success || !strings.Contains(res.Reason, "cancelled") {
		t.Fatal("cancelled workflow was not refused")
	}
}
