package svsh

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type testBackend struct {
	targets  []string
	allowed  map[string]bool
	shell    string
	capture  bool
	commands []Command
	output   io.Writer
}

func (b *testBackend) SelectedTargets() []string { return append([]string(nil), b.targets...) }
func (b *testBackend) SelectTargets(_ context.Context, ids []string) error {
	for _, id := range ids {
		if b.allowed != nil && !b.allowed[id] {
			return fmt.Errorf("outside scope: %s", id)
		}
	}
	b.targets = append([]string(nil), ids...)
	return nil
}
func (b *testBackend) WildcardTargets(context.Context) ([]Target, error) {
	var out []Target
	for id := range b.allowed {
		out = append(out, Target{ID: id, Name: id})
	}
	return out, nil
}
func (b *testBackend) ResolveTarget(_ context.Context, id string) (Target, error) {
	if b.allowed != nil && !b.allowed[id] {
		return Target{}, fmt.Errorf("outside scope")
	}
	return Target{ID: id, Name: id}, nil
}
func (b *testBackend) Shell() string {
	if b.shell == "" {
		return "cmd"
	}
	return b.shell
}
func (b *testBackend) Capture() bool     { return b.capture }
func (b *testBackend) SetCapture(v bool) { b.capture = v }
func (b *testBackend) Execute(ctx context.Context, c Command, emit func(ActionResult)) error {
	b.commands = append(b.commands, c)
	if c.Verb == "use" {
		b.shell = c.Text
		return nil
	}
	if c.Text == "block" {
		<-ctx.Done()
		return ctx.Err()
	}
	for _, id := range b.targets {
		exit := 0
		if c.Text == "fail" {
			exit = 7
		}
		stdout := fmt.Sprintf("%s:%s:%s\n", id, c.Text, c.Parameters["TAG"])
		if b.output != nil {
			fmt.Fprint(b.output, stdout)
		}
		emit(ActionResult{TargetID: id, Host: id, Type: "run_command", Stdout: stdout, ExitCode: exit, Reason: fmt.Sprintf("exit=%d", exit)})
	}
	return nil
}
func testRunner() Runner {
	return Runner{NewBackend: func(_ context.Context, j Job, w io.Writer) (Backend, error) {
		allowed := map[string]bool{}
		for _, id := range j.Targets {
			allowed[id] = true
		}
		return &testBackend{allowed: allowed, output: w}, nil
	}}
}

func TestReaderRetainsHeredocBlankLinesAndComments(t *testing.T) {
	e := New(&testBackend{}, Options{})
	for _, line := range []string{"script powershell until EOT", "# comment", "", "Write-Output hi"} {
		e.ExecLine(line)
	}
	if !e.State().InHeredoc || strings.Join(e.heredocLines, "\n") != "# comment\n\nWrite-Output hi" {
		t.Fatal("top-level body changed")
	}
	e = New(&testBackend{}, Options{})
	for _, line := range []string{"for X in 1", "script powershell until EOT", "# comment", "", "Write-Output hi", "EOT"} {
		e.ExecLine(line)
	}
	if !strings.Contains(strings.Join(e.blocks[0].body, "\n"), "# comment\n\nWrite-Output hi") {
		t.Fatal("nested body changed")
	}
}
func TestJobKindsShareInterpreterAndIsolateState(t *testing.T) {
	runner := testRunner()
	for _, kind := range []string{"script_job", "software_distribution", "dashboard"} {
		result := runner.Run(context.Background(), Job{ID: kind, Kind: kind, Targets: []string{"a", "b"}, Parameters: map[string]string{"TAG": kind}, Script: "foreach host in *\necho ${TAG}\nend\n"})
		if result.Status != "success" || result.TotalActions != 2 || !strings.Contains(result.Output, "echo "+kind) || result.Variables["TAG"] != kind {
			t.Fatalf("%s: %+v", kind, result)
		}
	}
	result := runner.Run(context.Background(), Job{Targets: []string{"a"}, Script: "vars\n"})
	if _, ok := result.Variables["TAG"]; ok {
		t.Fatal("parameters leaked between jobs")
	}
}
func TestStopAndContinueReportHonestAggregateFailure(t *testing.T) {
	for _, policy := range []string{"stop", "continue"} {
		result := testRunner().Run(context.Background(), Job{Targets: []string{"a"}, Script: "on-error " + policy + "\nfail\necho later\n"})
		want := 1
		if policy == "continue" {
			want = 2
		}
		if result.Status != "failed" || result.ExitCode == 0 || result.TotalActions != want || result.FailedActions != 1 {
			t.Fatalf("%s: %+v", policy, result)
		}
	}
}
func TestJobCancellationAndOutputLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	result := testRunner().Run(ctx, Job{Targets: []string{"a"}, Script: "wait 600\n"})
	if result.Status != "cancelled" || time.Since(start) > time.Second {
		t.Fatalf("cancellation %+v", result)
	}
	result = testRunner().Run(context.Background(), Job{Targets: []string{"a"}, MaxOutputBytes: 32, Script: "echo " + strings.Repeat("x", 1000)})
	if len(result.Output) > 32 || !result.OutputTruncated {
		t.Fatal("job output unbounded")
	}
}
func TestIncompleteInvalidAndRecursiveSourceFail(t *testing.T) {
	for _, script := range []string{"for X in 1\nlog hi", "script cmd until END\necho hi", "let invalid", "on-error maybe", "end"} {
		result := testRunner().Run(context.Background(), Job{Targets: []string{"a"}, Script: script})
		if result.Status != "failed" {
			t.Fatalf("accepted invalid script %q: %+v", script, result)
		}
	}
	runner := testRunner()
	runner.LoadFile = func(context.Context, string) ([]byte, error) { return []byte("source recursive.svsh\n"), nil }
	result := runner.Run(context.Background(), Job{Targets: []string{"a"}, ScriptFile: "recursive.svsh"})
	if result.Status != "failed" || !strings.Contains(result.Error, "depth") {
		t.Fatalf("recursive source %+v", result)
	}
}
func TestSourceAliasAndNativeExecUseSameState(t *testing.T) {
	b := &testBackend{targets: []string{"a"}, allowed: map[string]bool{"a": true}}
	files := map[string]string{"vars.svsh": "let REGION = apac\nalias hi = echo ${REGION}\n", "check.ps1": "Write-Host '${REGION}'\n# comment\n\nWrite-Output done"}
	e := New(b, Options{LoadFile: func(_ context.Context, name string) ([]byte, error) {
		data, ok := files[name]
		if !ok {
			return nil, fmt.Errorf("missing")
		}
		return []byte(data), nil
	}})
	result := e.Run(context.Background(), strings.NewReader("source vars.svsh\nhi\nexec check.ps1\n"))
	want, shell := BuildShellCommand("powershell", strings.ReplaceAll(files["check.ps1"], "${REGION}", "apac"))
	if result.Status != "success" || len(b.commands) != 2 || b.commands[0].Text != "echo apac" || b.commands[1].Text != want || b.commands[1].Shell != shell {
		t.Fatalf("source/exec mismatch: %+v %+v", result, b.commands)
	}
}
func TestConcurrentJobsDoNotShareVariablesOrOutput(t *testing.T) {
	runner := testRunner()
	var wg sync.WaitGroup
	for _, tag := range []string{"first", "second"} {
		tag := tag
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := runner.Run(context.Background(), Job{Targets: []string{tag}, Parameters: map[string]string{"TAG": tag}, Script: "echo ${TAG}"})
			other := "first"
			if tag == other {
				other = "second"
			}
			if result.Status != "success" || strings.Contains(result.Output, other) {
				t.Errorf("job state leaked: %+v", result)
			}
		}()
	}
	wg.Wait()
}
func TestInteractiveEngineWritesOnlyConfiguredWriter(t *testing.T) {
	var output bytes.Buffer
	e := New(&testBackend{}, Options{Output: &output})
	e.ExecLine("let TAG = isolated")
	e.ExecLine("log ${TAG}")
	if !strings.Contains(output.String(), "isolated") {
		t.Fatal("session writer missing output")
	}
}

func TestVariableAndStoredActionLimitsAreExplicit(t *testing.T) {
	e := New(&testBackend{}, Options{MaxVariableBytes: 8})
	result := e.Run(context.Background(), strings.NewReader("let TAG = exceeds-eight-bytes"))
	if result.Status != "failed" {
		t.Fatal("oversized variable accepted")
	}
	e = New(&testBackend{}, Options{MaxResultBytes: 16, MaxVariableBytes: 16})
	e.RecordResult(ActionResult{Stdout: strings.Repeat("x", 1000)})
	result = e.Run(context.Background(), strings.NewReader(""))
	if !result.ActionsTruncated || len(result.Actions[0].Stdout) > 16 || len(result.Variables["stdout"]) > 16 {
		t.Fatal("stored action/variable data unbounded")
	}
}
