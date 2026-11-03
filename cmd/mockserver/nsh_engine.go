package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/aegisoneuem/aegis_one_agent/pkg/nsh"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// Grammar lives only in pkg/nsh; this adapter binds the existing mock transport.
type scriptEngine struct {
	core *nsh.Engine
	vars map[string]string
}

func newScriptEngine(s *nshSession) *scriptEngine {
	core := nsh.New(&mockNSHBackend{s: s}, nsh.Options{Output: sessionWriter{s: s}, LoadFile: localNSHFile, LookupEnv: os.Getenv, OpenSessionLog: func(path string) (io.WriteCloser, error) {
		return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	}})
	return &scriptEngine{core: core, vars: core.Variables()}
}
func localNSHFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, nsh.DefaultMaxSourceBytes+1))
	if err == nil && len(data) > nsh.DefaultMaxSourceBytes {
		return nil, fmt.Errorf("script exceeds source byte limit")
	}
	return data, err
}
func (e *scriptEngine) syncVariables() {
	for k, v := range e.vars {
		e.core.SetVariable(k, v)
	}
}
func (e *scriptEngine) execLine(line string) bool {
	e.syncVariables()
	ok := e.core.ExecLine(line)
	e.vars = e.core.Variables()
	return ok
}
func (e *scriptEngine) expand(text string) string    { e.syncVariables(); return e.core.Expand(text) }
func (e *scriptEngine) variables() map[string]string { return e.core.Variables() }
func (e *scriptEngine) recordLastResult(host, stdout, stderr, reason string, exit int, duration int64) {
	e.core.RecordResult(nsh.ActionResult{Host: host, Stdout: stdout, Stderr: stderr, Reason: reason, ExitCode: exit, DurationMs: duration})
	e.vars = e.core.Variables()
}
func buildShellCommand(shell, content string) (string, string) {
	return nsh.BuildShellCommand(shell, content)
}
func splitCSV(text string) []string {
	var out []string
	for _, p := range strings.Split(text, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

type sessionWriter struct {
	s      *nshSession
	stderr bool
}

func (w sessionWriter) Write(p []byte) (int, error) { return w.s.writer(w.stderr).Write(p) }
func (s *nshSession) writer(stderr bool) io.Writer {
	if stderr && s.errorOutput != nil {
		return s.errorOutput
	}
	if s.output != nil {
		return s.output
	}
	if stderr {
		return os.Stderr
	}
	return os.Stdout
}
func (s *nshSession) printf(format string, args ...any) {
	fmt.Fprintf(s.writer(false), format, args...)
}
func (s *nshSession) println(args ...any) { fmt.Fprintln(s.writer(false), args...) }
func (s *nshSession) print(args ...any)   { fmt.Fprint(s.writer(false), args...) }
func (s *nshSession) recordAction(r nsh.ActionResult) {
	if s.emit != nil {
		s.emit(r)
	} else if s.engine != nil {
		s.engine.core.RecordResult(r)
		s.engine.vars = s.engine.core.Variables()
	}
}
func (s *nshSession) recordStatus(id string, st *pb.ActionStatus) {
	exit := int(st.ExitCode)
	if st.Phase == pb.ActionPhase_ACTION_PHASE_FAILED && exit == 0 {
		exit = 1
	}
	if s.jobMode && st.Phase == pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE && exit == 0 {
		exit = 1
	}
	s.recordAction(nsh.ActionResult{TargetID: id, Host: s.srv.hostnameOf(id), ActionID: st.ActionId, Type: st.ActionType, ExitCode: exit, Stdout: parseStdout(st.Detail), Stderr: parseStderr(st.Detail), Reason: st.Reason, DurationMs: parseDuration(st.Detail), UploadedPath: parseUploaded(st.Detail)})
}

type mockNSHBackend struct{ s *nshSession }

func (b *mockNSHBackend) SelectedTargets() []string { return append([]string(nil), b.s.targets...) }
func (b *mockNSHBackend) SelectTargets(_ context.Context, ids []string) error {
	for _, id := range ids {
		if b.s.allowedTargets != nil && !b.s.allowedTargets[id] {
			return fmt.Errorf("target %s is outside job scope", id)
		}
	}
	b.s.targets = append([]string(nil), ids...)
	return nil
}
func (b *mockNSHBackend) WildcardTargets(ctx context.Context) ([]nsh.Target, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.s.srv.liveMu.Lock()
	var ids []string
	for id := range b.s.srv.liveAgents {
		if b.s.allowedTargets == nil || b.s.allowedTargets[id] {
			ids = append(ids, id)
		}
	}
	b.s.srv.liveMu.Unlock()
	if b.s.jobMode {
		ids = ids[:0]
		for id := range b.s.allowedTargets {
			ids = append(ids, id)
		}
	}
	var out []nsh.Target
	for _, id := range ids {
		out = append(out, nsh.Target{ID: id, Name: b.s.srv.hostnameOf(id)})
	}
	return out, nil
}
func (b *mockNSHBackend) ResolveTarget(ctx context.Context, token string) (nsh.Target, error) {
	if err := ctx.Err(); err != nil {
		return nsh.Target{}, err
	}
	id, err := b.s.srv.resolveKnownTarget(token)
	if err != nil {
		return nsh.Target{}, err
	}
	if b.s.allowedTargets != nil && !b.s.allowedTargets[id] {
		return nsh.Target{}, fmt.Errorf("target outside job scope: %s", token)
	}
	if !b.s.srv.isOnline(id) {
		return nsh.Target{}, fmt.Errorf("target is offline: %s", token)
	}
	return nsh.Target{ID: id, Name: b.s.srv.hostnameOf(id)}, nil
}
func (b *mockNSHBackend) Shell() string     { return b.s.shell }
func (b *mockNSHBackend) Capture() bool     { return b.s.capture }
func (b *mockNSHBackend) SetCapture(v bool) { b.s.capture = v }
func (b *mockNSHBackend) Execute(ctx context.Context, c nsh.Command, emit func(nsh.ActionResult)) error {
	s := b.s
	if err := ctx.Err(); err != nil {
		return err
	}
	savedCtx, savedParams, savedEmit := s.ctx, s.parameters, s.emit
	s.ctx, s.parameters = ctx, c.Parameters
	var mu sync.Mutex
	count := 0
	s.emit = func(r nsh.ActionResult) { mu.Lock(); count++; mu.Unlock(); emit(r) }
	defer func() { s.ctx, s.parameters, s.emit = savedCtx, savedParams, savedEmit }()
	savedShell := s.shell
	if c.Shell != "" {
		s.shell = c.Shell
	}
	defer func() { s.shell = savedShell }()
	action := false
	switch c.Verb {
	case "run":
		action = true
		s.verbRun(c.Text, c.Fanout)
	case "fanout":
		action = true
		s.verbRun(c.Text, true)
	case "grab":
		action = true
		s.verbGrab(c.Text)
	case "pull":
		action = true
		s.verbPull(c.Text)
	case "copy", "cp":
		action = true
		s.verbFile("copy", c.Text)
	case "move", "mv":
		action = true
		s.verbFile("move", c.Text)
	case "delete", "del", "rm":
		action = true
		s.verbFile("delete", c.Text)
	case "mkdir", "md":
		action = true
		s.verbFile("mkdir", c.Text)
	case "touch":
		action = true
		s.verbFile("touch", c.Text)
	case "cd":
		selection := strings.TrimSpace(c.Text)
		if strings.HasPrefix(selection, `\\`) && selection != `\\` {
			for _, token := range strings.Split(strings.TrimPrefix(selection, `\\`), ",") {
				id, err := s.srv.resolveKnownTarget(strings.TrimPrefix(strings.TrimSpace(token), `\\`))
				if err != nil {
					return err
				}
				if s.allowedTargets != nil && !s.allowedTargets[id] {
					return fmt.Errorf("target %s is outside job scope", id)
				}
			}
		} else if selection != "*" && selection != `\\` {
			action = true
		}
		before := append([]string(nil), s.targets...)
		s.verbCD(c.Text)
		if s.allowedTargets != nil {
			for _, id := range s.targets {
				if !s.allowedTargets[id] {
					s.targets = before
					return fmt.Errorf("target %s is outside job scope", id)
				}
			}
		}
	case "pwd":
		action = true
		s.changeWorkingDir("")
	case "use":
		choice := strings.ToLower(strings.TrimSpace(c.Text))
		switch choice {
		case "cmd", "powershell", "ps", "pwsh", "bash", "sh", "zsh":
			s.verbUse(choice)
			savedShell = s.shell
		default:
			return fmt.Errorf("unsupported shell: %s", choice)
		}
	case "set":
		if err := s.setNSHOption(c.Text); err != nil {
			return err
		}
	case "hosts", "search":
		if _, err := parseHostQuery(c.Text); err != nil {
			return err
		}
		s.verbHosts(c.Text)
	case "inventory":
		s.verbInventory(c.Text)
	case "cat":
		s.verbCat(c.Text)
	case "help":
		s.verbHelp(c.Text)
	case "history":
		for i, line := range s.history {
			s.printf("  %3d %s\n", i+1, line)
		}
	case "shell":
		if s.jobMode {
			return fmt.Errorf("persistent shell requires an interactive terminal")
		}
		s.verbShell()
	case "job":
		if s.jobMode {
			return fmt.Errorf("nested job launch is not available")
		}
		return s.verbJob(c.Text)
	default:
		return fmt.Errorf("unsupported NSH command %s", c.Verb)
	}
	mu.Lock()
	none := count == 0
	mu.Unlock()
	if action && none {
		return fmt.Errorf("%s did not execute; inspect output", c.Verb)
	}
	return nil
}
func (s *nshSession) setNSHOption(rest string) error {
	key, value := splitFirst(rest)
	key = strings.ToLower(key)
	switch key {
	case "file-workers", "file-max-files", "file-max-bytes", "timeout", "max-output":
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n <= 0 {
			return fmt.Errorf("%s needs a positive number", key)
		}
		if key == "file-workers" && n > 32 {
			return fmt.Errorf("file-workers must be 1..32")
		}
		if key == "timeout" && n > 1<<31-11 {
			return fmt.Errorf("timeout too large")
		}
	case "capture":
		switch value {
		case "on", "off", "true", "false", "1", "0":
		default:
			return fmt.Errorf("capture must be on/off")
		}
	case "output-dir":
		if _, err := safeNSHUploadPath(value + "/probe.out"); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown set option: %s", key)
	}
	if !s.setFileOption(key, value) {
		s.verbSet(rest)
	}
	return nil
}
