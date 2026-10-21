//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

// runShellSession dials ShellSession, spawns cmd.exe (LocalSystem), pumps stdin
// in from the server, stdout+stderr out to the server. Enforces idle + max
// timeouts and writes an append-only transcript the agent persists for audit.
// NOT RDP / NOT a graphical session: pure text streaming, BladeLogic nexec level.
// v1 is line-based (plain pipes); v2 can switch to ConPTY for arrow keys / colors.
func runShellSession(ctx context.Context, client pb.AgentControlPlaneClient, outbox chan<- *pb.AgentMessage, deviceID, secureDir, cmdID string, spec *pb.OpenShellSpec) {
	idleTimeout := time.Duration(spec.IdleTimeoutSeconds) * time.Second
	if idleTimeout <= 0 {
		idleTimeout = shellDefaultIdleTimeout * time.Second
	}
	maxDuration := time.Duration(spec.MaxDurationSeconds) * time.Second
	if maxDuration <= 0 {
		maxDuration = shellDefaultMaxDuration * time.Second
	}

	// Session-scoped context: bounded by max_duration and the stream itself.
	sessionCtx, cancel := context.WithTimeout(ctx, maxDuration)
	defer cancel()

	stream, err := client.ShellSession(sessionCtx)
	if err != nil {
		slog.Error("open_shell: could not open stream", "command_id", cmdID, "error", err.Error())
		finishShell(outbox, deviceID, cmdID, pb.ActionPhase_ACTION_PHASE_FAILED, "could not open shell stream: "+err.Error())
		return
	}
	// FIRST chunk from agent → server MUST carry session_id so the server can
	// bind the stream to the signed open_shell. No bytes yet.
	if err := stream.Send(&pb.ShellOutput{SessionId: spec.SessionId}); err != nil {
		slog.Error("open_shell: first send failed", "command_id", cmdID, "error", err.Error())
		finishShell(outbox, deviceID, cmdID, pb.ActionPhase_ACTION_PHASE_FAILED, "first send: "+err.Error())
		return
	}

	// Spawn cmd.exe with piped stdio. LocalSystem — matches the service account.
	// CreationFlags: CREATE_NEW_PROCESS_GROUP so a Ctrl+C from the server reaches
	// only this process group, not the agent service.
	shell := exec.CommandContext(sessionCtx, "cmd.exe")
	shell.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windowsCreateNewProcessGroup | windowsCreateNoWindow,
	}
	stdin, err := shell.StdinPipe()
	if err != nil {
		finishShell(outbox, deviceID, cmdID, pb.ActionPhase_ACTION_PHASE_FAILED, "stdin pipe: "+err.Error())
		return
	}
	stdout, err := shell.StdoutPipe()
	if err != nil {
		finishShell(outbox, deviceID, cmdID, pb.ActionPhase_ACTION_PHASE_FAILED, "stdout pipe: "+err.Error())
		return
	}
	stderr, err := shell.StderrPipe()
	if err != nil {
		finishShell(outbox, deviceID, cmdID, pb.ActionPhase_ACTION_PHASE_FAILED, "stderr pipe: "+err.Error())
		return
	}
	if err := shell.Start(); err != nil {
		finishShell(outbox, deviceID, cmdID, pb.ActionPhase_ACTION_PHASE_FAILED, "start cmd.exe: "+err.Error())
		return
	}
	slog.Info("open_shell: shell started", "command_id", cmdID, "pid", shell.Process.Pid)

	// Transcript: a tamper-evident local log of every byte in and out. Written to
	// SKVC\logs\shell\<cmd_id>.transcript. The server can collect it afterwards
	// via collect_file; here we just write it reliably.
	transcript := openTranscript(secureDir, cmdID)
	defer transcript.close()

	// Idle tracking: a bump() on every input OR output byte; a goroutine kills
	// the shell when the window elapses with no activity.
	var (
		lastActivity = time.Now()
		activityMu   sync.Mutex
		closeReason  = "exited"
		reasonMu     sync.Mutex
	)
	bump := func() {
		activityMu.Lock()
		lastActivity = time.Now()
		activityMu.Unlock()
	}
	setReason := func(r string) {
		reasonMu.Lock()
		if closeReason == "exited" { // first non-default sticks
			closeReason = r
		}
		reasonMu.Unlock()
	}
	getReason := func() string {
		reasonMu.Lock()
		defer reasonMu.Unlock()
		return closeReason
	}

	// Idle watchdog.
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-sessionCtx.Done():
				return
			case <-t.C:
				activityMu.Lock()
				idle := time.Since(lastActivity)
				activityMu.Unlock()
				if idle >= idleTimeout {
					slog.Warn("open_shell: idle timeout — killing shell", "command_id", cmdID, "idle", idle.String())
					setReason("idle_timeout")
					_ = shell.Process.Kill()
					return
				}
			}
		}
	}()

	// Server → agent (stdin + control). The session binding was done above with
	// the first SessionId chunk; everything on this receive leg is already bound.
	go func() {
		defer stdin.Close()
		for {
			in, err := stream.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					slog.Info("open_shell: input stream ended", "command_id", cmdID, "error", err.Error())
				}
				return
			}
			if in.Control == "close" {
				setReason("close")
				_ = shell.Process.Kill()
				return
			}
			if in.Control == "break" {
				// Best-effort Ctrl+C. GenerateConsoleCtrlEvent would need shared
				// console; the LocalSystem service has none, so Kill is the honest
				// path on break too.
				setReason("break")
				_ = shell.Process.Kill()
				return
			}
			if len(in.Stdin) > 0 {
				bump()
				transcript.writeIn(in.Stdin)
				if _, err := stdin.Write(in.Stdin); err != nil {
					slog.Warn("open_shell: stdin write failed", "command_id", cmdID, "error", err.Error())
					return
				}
			}
		}
	}()

	// Agent → server (stdout + stderr), pumped by a shared sender. gRPC Send is
	// not safe for concurrent use, so one goroutine serializes it.
	type outChunk struct{ out, errBytes []byte }
	outCh := make(chan outChunk, 64)
	var pumpWG sync.WaitGroup
	pumpWG.Add(2)
	pump := func(src io.Reader, isStderr bool) {
		defer pumpWG.Done()
		buf := make([]byte, 4096)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				if isStderr {
					outCh <- outChunk{errBytes: chunk}
				} else {
					outCh <- outChunk{out: chunk}
				}
			}
			if err != nil {
				return
			}
		}
	}
	go pump(stdout, false)
	go pump(stderr, true)

	// Close outCh when both pumps finish → the sender below exits cleanly.
	go func() { pumpWG.Wait(); close(outCh) }()

	// Sender: serialize outCh → stream.Send, record transcript, bump activity.
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		for c := range outCh {
			bump()
			msg := &pb.ShellOutput{Stdout: c.out, Stderr: c.errBytes}
			if len(c.out) > 0 {
				transcript.writeOut(c.out, false)
			}
			if len(c.errBytes) > 0 {
				transcript.writeOut(c.errBytes, true)
			}
			if err := stream.Send(msg); err != nil {
				slog.Warn("open_shell: send failed", "command_id", cmdID, "error", err.Error())
				return
			}
		}
	}()

	waitErr := shell.Wait()
	exitCode := 0
	if waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}
	if sessionCtx.Err() == context.DeadlineExceeded {
		setReason("max_duration")
	}
	// Wait for the sender to drain, then send the final chunk and close.
	<-sendDone
	reason := getReason()
	_ = stream.Send(&pb.ShellOutput{Closed: true, ExitCode: int32(exitCode), Reason: reason})
	_ = stream.CloseSend()

	slog.Info("open_shell: session closed", "command_id", cmdID, "exit_code", exitCode, "reason", reason,
		"transcript", transcript.path)
	phase := pb.ActionPhase_ACTION_PHASE_SUCCESS
	if reason != "exited" && reason != "close" {
		phase = pb.ActionPhase_ACTION_PHASE_FAILED
	}
	finishShell(outbox, deviceID, cmdID, phase, fmt.Sprintf("shell closed (exit=%d, %s), transcript=%s", exitCode, reason, transcript.path))
}

// finishShell emits the terminal ActionStatus for the shell session.
func finishShell(outbox chan<- *pb.AgentMessage, deviceID, cmdID string, phase pb.ActionPhase, reason string) {
	st := simpleActionStatus(cmdID, actionOpenShell, deviceID, "", phase, reason)
	outbox <- &pb.AgentMessage{Payload: &pb.AgentMessage_ActionStatus{ActionStatus: st}}
	// Keep proto import used in both files even if the method evolves.
	_ = proto.Marshal
}

// --- Transcript: append-only local log of in/out bytes for audit. ---

type shellTranscript struct {
	path string
	f    *os.File
	mu   sync.Mutex
}

// openTranscript opens <SKVC>\logs\shell\<cmd_id>.transcript (best-effort —
// a shell session should not fail just because we couldn't log; log loud). Was
// bitten before: a relative path landed in C:\Windows\System32 because the
// LocalSystem service's CWD is there. ALWAYS anchor to secureDir (SKVC root).
func openTranscript(secureDir, cmdID string) *shellTranscript {
	dir := filepath.Join(secureDir, "logs", "shell")
	_ = os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, sanitize(cmdID)+".transcript")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		slog.Error("open_shell: could not open transcript (session continues)", "path", path, "error", err.Error())
		return &shellTranscript{path: path}
	}
	fmt.Fprintf(f, "=== shell session %s opened at %s ===\n", cmdID, time.Now().UTC().Format(time.RFC3339))
	return &shellTranscript{path: path, f: f}
}

func (t *shellTranscript) writeIn(b []byte) {
	if t == nil || t.f == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintf(t.f, "[IN  %s] ", time.Now().UTC().Format("15:04:05"))
	t.f.Write(b)
	if len(b) == 0 || b[len(b)-1] != '\n' {
		t.f.Write([]byte{'\n'})
	}
}

func (t *shellTranscript) writeOut(b []byte, isStderr bool) {
	if t == nil || t.f == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	tag := "OUT"
	if isStderr {
		tag = "ERR"
	}
	fmt.Fprintf(t.f, "[%s %s] ", tag, time.Now().UTC().Format("15:04:05"))
	t.f.Write(b)
	if len(b) == 0 || b[len(b)-1] != '\n' {
		t.f.Write([]byte{'\n'})
	}
}

func (t *shellTranscript) close() {
	if t == nil || t.f == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintf(t.f, "=== closed at %s ===\n", time.Now().UTC().Format(time.RFC3339))
	t.f.Close()
}

// Windows CreateProcess flags we need. Kept here so the whole shell session file
// is self-contained; constants are stable across Windows versions.
const (
	windowsCreateNewProcessGroup = 0x00000200
	windowsCreateNoWindow        = 0x08000000
)
