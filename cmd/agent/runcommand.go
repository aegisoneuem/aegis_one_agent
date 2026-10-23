package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

// Defaults for run_command (overridable per spec).
const (
	rcDefaultTimeout   = 300        // seconds
	rcDefaultMaxOutput = 1 << 20    // 1 MiB inline capture per stream
	rcHardMaxOutput    = 100 << 20  // never buffer more than 100 MiB inline (DoS brake)
)

// handleRunCommandCmd is the entry for a signed "run_command" task — a one-shot
// command execution that returns stdout+stderr+exit as ActionStatus. This is
// the BigFix WebUI / BladeLogic Type-1 equivalent and the atom the mock's NSH
// REPL (and future console-authored tasks) fire for every ad-hoc query. Runs
// on its own goroutine so a slow command never blocks the receiver, and never
// touches the softdist/patch install gate (nothing to install here).
func (d *dispatcher) handleRunCommandCmd(ctx context.Context, client pb.AgentControlPlaneClient, cmd *pb.Command) {
	var spec pb.RunCommandSpec
	if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
		slog.Error("run_command: invalid payload", "command_id", cmd.CommandId, "error", err.Error())
		send(ctx, d.outbox, buildCommandResultMessage(cmd.CommandId, "failed", "invalid run_command payload"))
		return
	}
	if spec.Command == "" {
		send(ctx, d.outbox, actionStatusMsg(simpleActionStatus(cmd.CommandId, actionRunCommand, d.cfg.DeviceID, "",
			pb.ActionPhase_ACTION_PHASE_FAILED, "run_command: empty command text")))
		return
	}
	slog.Info("run_command: accepted", "command_id", cmd.CommandId, "shell", spec.Shell,
		"timeout_s", spec.TimeoutSeconds, "upload_as", spec.UploadAs)
	send(ctx, d.outbox, actionStatusMsg(simpleActionStatus(cmd.CommandId, actionRunCommand, d.cfg.DeviceID, "",
		pb.ActionPhase_ACTION_PHASE_RUNNING, "running")))

	go func() {
		defer recoverLog("run-command")
		d.runCommandWorker(ctx, client, cmd.CommandId, &spec)
	}()
}

// runCommandWorker executes the command, uploads stdout as a file when
// requested, and streams the terminal ActionStatus with a structured JSON
// Detail (RunCommandResult-shaped) the mock REPL / console can parse.
func (d *dispatcher) runCommandWorker(ctx context.Context, client pb.AgentControlPlaneClient, cmdID string, spec *pb.RunCommandSpec) {
	started := time.Now()
	timeout := time.Duration(spec.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = rcDefaultTimeout * time.Second
	}
	maxOut := spec.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = rcDefaultMaxOutput
	}
	if maxOut > rcHardMaxOutput {
		maxOut = rcHardMaxOutput
	}

	// Substitute ${name} tokens using the same engine softdist uses (consistent
	// across every task type that takes params).
	expanded := softdist.Substitute(spec.Command, spec.Parameters)

	cwd := spec.WorkingDir
	if cwd == "" {
		cwd = filepath.Join(d.cfg.SecureDir, "tmp")
		_ = os.MkdirAll(cwd, 0o755)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out := runOneCommand(runCtx, expanded, spec.Shell, cwd, spec.Parameters, maxOut)
	if out.FullStdoutPath != "" {
		defer os.Remove(out.FullStdoutPath)
	}
	out.DurationMs = time.Since(started).Milliseconds()

	// Local transcript — append-only audit record of every run_command on this
	// endpoint. Lives under SKVC\logs\nsh so the console can collect it later
	// via collect_file if needed. Best-effort; a failure here does not change
	// the command result.
	writeRunCommandTranscript(d.cfg.SecureDir, cmdID, expanded, out)

	// Optional stdout upload: write the FULL stdout to a temp file (not just
	// inline cap) and stream it to the server via UploadContent. Full-stdout
	// capture is a separate, unbounded-size buffer written to disk during run;
	// the inline Stdout stays at maxOut for the ActionStatus.
	if spec.UploadAs != "" && out.FullStdoutPath != "" {
		name := spec.UploadAs
		if n, stored, err := uploadFile(ctx, client, cmdID, name, out.FullStdoutPath, 0, 0); err != nil {
			slog.Warn("run_command: upload failed", "command_id", cmdID, "error", err.Error())
			out.Reason += " (upload failed: " + err.Error() + ")"
		} else {
			out.UploadedPath = stored
			slog.Info("run_command: stdout uploaded", "command_id", cmdID, "bytes", n, "stored", stored)
		}
		_ = os.Remove(out.FullStdoutPath)
	}

	// Shape a RunCommandResult JSON into ActionStatus.Detail so the REPL /
	// console can render it structurally.
	result := &pb.RunCommandResult{
		ExitCode:     int32(out.ExitCode),
		ShellUsed:    out.ShellUsed,
		StdoutBytes:  out.StdoutBytes,
		StderrBytes:  out.StderrBytes,
		Stdout:       out.Stdout,
		Stderr:       out.Stderr,
		Truncated:    out.Truncated,
		UploadedPath: out.UploadedPath,
		DurationMs:   out.DurationMs,
	}
	detail, _ := json.Marshal(result)

	phase := pb.ActionPhase_ACTION_PHASE_SUCCESS
	switch {
	case out.NotApplicable:
		phase = pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE
	case out.ExitCode != 0 || out.Reason != "":
		phase = pb.ActionPhase_ACTION_PHASE_FAILED
	}
	reason := out.Reason
	if reason == "" {
		if phase == pb.ActionPhase_ACTION_PHASE_SUCCESS {
			reason = fmt.Sprintf("exit=0 (%s, %dms)", out.ShellUsed, out.DurationMs)
		} else {
			reason = fmt.Sprintf("exit=%d (%s, %dms)", out.ExitCode, out.ShellUsed, out.DurationMs)
		}
	}

	sendStatus(d.outbox, &pb.ActionStatus{
		ActionId: cmdID, ActionType: actionRunCommand, DeviceId: d.cfg.DeviceID,
		Phase: phase, ExitCode: int32(out.ExitCode),
		Reason: reason, Detail: string(detail),
		StartedAt: started.Unix(), FinishedAt: time.Now().Unix(),
	})
	slog.Info("run_command: finished", "command_id", cmdID, "phase", phase.String(),
		"exit", out.ExitCode, "shell", out.ShellUsed, "stdout_bytes", out.StdoutBytes, "duration_ms", out.DurationMs)
}

// writeRunCommandTranscript appends an audit record of a run_command execution
// under <SKVC>\logs\nsh. Format: single line header with action_id, timestamp,
// shell, exit, duration — followed by the command text, stdout, stderr. The
// FULL stdout (not just inline cap) is captured when it was buffered to disk.
// Best-effort — never breaks the command result.
func writeRunCommandTranscript(secureDir, cmdID, commandText string, out runCommandOutput) {
	dir := filepath.Join(secureDir, "logs", "nsh")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	path := filepath.Join(dir, sanitize(cmdID)+".transcript")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().UTC().Format(time.RFC3339)
	fmt.Fprintf(f, "=== run_command %s @ %s (shell=%s, exit=%d, %dms) ===\n",
		cmdID, ts, out.ShellUsed, out.ExitCode, out.DurationMs)
	fmt.Fprintf(f, "CMD: %s\n", commandText)
	if out.Stdout != "" {
		fmt.Fprintln(f, "--- STDOUT ---")
		fmt.Fprint(f, out.Stdout)
		if !strings.HasSuffix(out.Stdout, "\n") {
			fmt.Fprintln(f)
		}
	}
	if out.Stderr != "" {
		fmt.Fprintln(f, "--- STDERR ---")
		fmt.Fprint(f, out.Stderr)
		if !strings.HasSuffix(out.Stderr, "\n") {
			fmt.Fprintln(f)
		}
	}
	if out.Reason != "" {
		fmt.Fprintf(f, "--- REASON --- %s\n", out.Reason)
	}
	fmt.Fprintln(f, "=== end ===")
}

// runCommandOutput is what the OS-specific runner returns to the worker above.
type runCommandOutput struct {
	ExitCode       int
	ShellUsed      string // "cmd" | "powershell" | "pwsh" | "bash" | "sh" | "zsh"
	Stdout         string // inline (cap-bounded)
	Stderr         string // inline (cap-bounded)
	StdoutBytes    int64  // total stdout bytes (even if inline truncated)
	StderrBytes    int64  // total stderr bytes
	Truncated      bool
	FullStdoutPath string // when upload_as asked for it — the on-disk temp
	UploadedPath   string // populated by caller after a successful upload
	DurationMs     int64  // populated by caller
	NotApplicable  bool   // shell not supported on this OS
	Reason         string // non-empty when the command could not be launched
}
