package softdist

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CommandStep is an author-supplied executable, not an implicit shell command.
// The same job token, ordinary parameters and verified package cwd are reused.
type CommandStep struct {
	Name, Command, Arguments string
	TimeoutSeconds           int32
	SuccessCodes             []int32
	UseSecureParameters      bool
}

type DeploymentSteps struct {
	Before, After, Rollback []CommandStep
}

func (steps DeploymentSteps) Validate() error {
	if len(steps.Before)+len(steps.After)+len(steps.Rollback) == 0 {
		return fmt.Errorf("workflow needs at least one pre/post/rollback command")
	}
	for _, list := range [][]CommandStep{steps.Before, steps.After, steps.Rollback} {
		if len(list) > 8 {
			return fmt.Errorf("workflow supports at most eight commands per stage")
		}
		for _, step := range list {
			if strings.TrimSpace(step.Name) == "" || len(step.Name) > 128 || strings.ContainsAny(step.Name, "\r\n\x00") {
				return fmt.Errorf("step needs a single-line name of 1-128 bytes")
			}
			if strings.TrimSpace(step.Command) == "" || strings.ContainsRune(step.Command, 0) || strings.ContainsRune(step.Arguments, 0) {
				return fmt.Errorf("step needs an executable and valid arguments")
			}
			if step.TimeoutSeconds < 0 || step.TimeoutSeconds > 3600 {
				return fmt.Errorf("step timeout must be between zero and 3600 seconds")
			}
			if len(step.SuccessCodes) > 32 {
				return fmt.Errorf("step supports at most 32 additional success codes")
			}
		}
	}
	return nil
}

// PublicFields includes even unselected steps: secrets must not leak into any
// public job metadata, even when an earlier step would prevent their execution.
func (steps DeploymentSteps) PublicFields() []string {
	var fields []string
	for _, list := range [][]CommandStep{steps.Before, steps.After, steps.Rollback} {
		for _, step := range list {
			fields = append(fields, step.Name, step.Command, step.Arguments)
		}
	}
	return fields
}

// RunWorkflow executes before -> main -> after, stopping at the first failure.
// Detection skips occur in the worker before entry; post-action detection and
// owned-bundle cleanup remain in the worker after return, including on failure.
func RunWorkflow(ctx context.Context, p Params, steps DeploymentSteps, progress func(stage string, index int, name string)) Result {
	return runWorkflow(ctx, p, steps, progress, Run)
}

func runWorkflow(ctx context.Context, p Params, steps DeploymentSteps, progress func(string, int, string), run func(context.Context, Params) Result) Result {
	if err := steps.Validate(); err != nil {
		return Result{Reason: err.Error()}
	}
	p.PublicMetadata = append(append([]string(nil), p.PublicMetadata...), steps.PublicFields()...)
	if err := ValidatePrivateInput(p); err != nil {
		return Result{Reason: err.Error()}
	}
	reboot := false
	runSteps := func(stage string, list []CommandStep) *Result {
		for i, step := range list {
			if err := ctx.Err(); err != nil {
				return &Result{Reason: stage + " cancelled", RebootRequired: reboot}
			}
			if progress != nil {
				progress(stage, i+1, step.Name)
			}
			// Copy only execution inputs. Product identifiers, installer success
			// codes and hash/signature settings belong to the main action only.
			timeout := time.Duration(step.TimeoutSeconds) * time.Second
			if timeout == 0 {
				timeout = 5 * time.Minute
			}
			cp := Params{Name: step.Name, Action: "install", InstallerType: "command",
				FilePath: step.Command, SilentArgs: step.Arguments, Timeout: timeout,
				SuccessCodes: step.SuccessCodes, WorkingDirectory: p.WorkingDirectory,
				Parameters: p.Parameters, Session: p.Session, MaxSecretBytes: p.MaxSecretBytes,
				Sensitive: p.Sensitive || len(p.SecureParameters) > 0}
			if step.UseSecureParameters {
				cp.SecureParameters = p.SecureParameters
			}
			res := run(ctx, cp)
			reboot = reboot || res.RebootRequired
			if !res.Success || res.NotApplicable || res.ExitCode == 1641 {
				res.Success, res.NotApplicable, res.RebootRequired = false, false, reboot
				res.Reason = fmt.Sprintf("%s step %d (%s) failed: %s", stage, i+1, step.Name, res.Reason)
				if res.ExitCode == 1641 {
					res.Reason = fmt.Sprintf("%s step %d initiated a reboot; remaining commands were not run", stage, i+1)
				}
				return &res
			}
		}
		return nil
	}
	// runRollback: best-effort cleanup after a failure. Each rollback step runs
	// even if an earlier one fails — the signed admin ordered the full sequence,
	// and a half-done rollback is worse than one that reports every failure.
	// Rollback failures append to the main reason so the console sees both.
	runRollback := func(trigger string) string {
		if len(steps.Rollback) == 0 {
			return ""
		}
		var failures []string
		for i, step := range steps.Rollback {
			if progress != nil {
				progress("rollback", i+1, step.Name)
			}
			timeout := time.Duration(step.TimeoutSeconds) * time.Second
			if timeout == 0 {
				timeout = 5 * time.Minute
			}
			// Rollback runs in a FRESH context so an already-cancelled parent
			// context (e.g. caller abort) does not immediately skip cleanup.
			rctx, cancel := context.WithTimeout(context.Background(), timeout)
			cp := Params{Name: step.Name, Action: "install", InstallerType: "command",
				FilePath: step.Command, SilentArgs: step.Arguments, Timeout: timeout,
				SuccessCodes: step.SuccessCodes, WorkingDirectory: p.WorkingDirectory,
				Parameters: p.Parameters, Session: p.Session, MaxSecretBytes: p.MaxSecretBytes,
				Sensitive: p.Sensitive || len(p.SecureParameters) > 0}
			if step.UseSecureParameters {
				cp.SecureParameters = p.SecureParameters
			}
			rres := run(rctx, cp)
			cancel()
			if !rres.Success {
				failures = append(failures, fmt.Sprintf("rollback step %d (%s) exit %d", i+1, step.Name, rres.ExitCode))
			}
		}
		if len(failures) == 0 {
			return fmt.Sprintf("; rollback after %s completed (%d step(s))", trigger, len(steps.Rollback))
		}
		return fmt.Sprintf("; rollback after %s partial — %s", trigger, strings.Join(failures, ", "))
	}

	if failure := runSteps("pre-deployment", steps.Before); failure != nil {
		failure.Reason += runRollback("pre-deployment failure")
		return *failure
	}
	if err := ctx.Err(); err != nil {
		return Result{Reason: "deployment cancelled" + runRollback("cancellation"), RebootRequired: reboot}
	}
	if progress != nil {
		progress("main", 0, p.Name)
	}
	res := run(ctx, p)
	reboot = reboot || res.RebootRequired
	res.RebootRequired = reboot
	if !res.Success || res.NotApplicable {
		res.Reason = "main deployment: " + res.Reason + runRollback("main failure")
		return res
	}
	if res.ExitCode == 1641 && len(steps.After) > 0 {
		res.Success = false
		res.Reason = "installer initiated a reboot; post-deployment commands were not run"
		return res
	}
	if failure := runSteps("post-deployment", steps.After); failure != nil {
		failure.Reason += runRollback("post-deployment failure")
		return *failure
	}
	res.RebootRequired = reboot
	res.Reason = fmt.Sprintf("deployment steps completed (main exit %d)", res.ExitCode)
	if reboot {
		res.Reason += "; restart required"
	}
	return res
}
