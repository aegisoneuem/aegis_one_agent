// Package svsh is the shared SVSH interpreter for terminals and non-interactive
// jobs. It has no agent transport, console login or process-global I/O.
package svsh

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

type Target struct{ ID, Name string }
type Command struct {
	Verb, Text, Shell string
	Fanout            bool
	Parameters        map[string]string
}

// Backend is session-scoped. Implementations authorize each target/action and
// emit its terminal result. Dispatch must observe ctx and finish its callbacks
// before returning. Engine parsing is single-owner; concurrent result emission
// is safe. SelectedTargets must return a copy.
type Backend interface {
	Execute(context.Context, Command, func(ActionResult)) error
	WildcardTargets(context.Context) ([]Target, error)
	ResolveTarget(context.Context, string) (Target, error)
	SelectedTargets() []string
	SelectTargets(context.Context, []string) error
	Shell() string
	Capture() bool
	SetCapture(bool)
}
type Options struct {
	Output                         io.Writer
	Parameters                     map[string]string
	LoadFile                       func(context.Context, string) ([]byte, error)
	LookupEnv                      func(string) string
	OpenSessionLog                 func(string) (io.WriteCloser, error)
	MaxActions, MaxSourceDepth     int
	MaxResultBytes, MaxSourceBytes int
	MaxVariableBytes               int
}

const DefaultMaxSourceBytes = 4 << 20

type ActionResult struct {
	TargetID     string `json:"target_id,omitempty"`
	Host         string `json:"host,omitempty"`
	ActionID     string `json:"action_id,omitempty"`
	Type         string `json:"type,omitempty"`
	ExitCode     int    `json:"exit_code"`
	Stdout       string `json:"stdout,omitempty"`
	Stderr       string `json:"stderr,omitempty"`
	Reason       string `json:"reason,omitempty"`
	UploadedPath string `json:"uploaded_path,omitempty"`
	DurationMs   int64  `json:"duration_ms,omitempty"`
	Summary      bool   `json:"summary,omitempty"`
}
type State struct {
	InHeredoc  bool
	Delimiter  string
	BlockDepth int
}
type Result struct {
	ID               string            `json:"id,omitempty"`
	Kind             string            `json:"kind,omitempty"`
	Status           string            `json:"status"` // success, failed, cancelled
	ExitCode         int               `json:"exit_code"`
	Error            string            `json:"error,omitempty"`
	Actions          []ActionResult    `json:"actions"`
	TotalActions     int               `json:"total_actions"`
	FailedActions    int               `json:"failed_actions"`
	Variables        map[string]string `json:"variables"`
	Output           string            `json:"output,omitempty"`
	OutputTruncated  bool              `json:"output_truncated,omitempty"`
	ActionsTruncated bool              `json:"actions_truncated,omitempty"`
	StartedAt        time.Time         `json:"started_at"`
	FinishedAt       time.Time         `json:"finished_at"`
}

func (e *Engine) Variables() map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]string, len(e.vars))
	for k, v := range e.vars {
		out[k] = v
	}
	return out
}
func (e *Engine) SetVariable(k, v string) {
	if len(k) > e.options.MaxVariableBytes || len(v) > e.options.MaxVariableBytes {
		e.fail(fmt.Errorf("variable %s exceeds byte limit", k))
		return
	}
	e.mu.Lock()
	e.vars[k] = v
	e.mu.Unlock()
}
func (e *Engine) RecordResult(r ActionResult) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !r.Summary {
		e.totalActions++
		if len(e.actions) < e.options.MaxActions {
			stored := r
			for _, field := range []*string{&stored.Stdout, &stored.Stderr, &stored.Reason, &stored.UploadedPath} {
				room := e.options.MaxResultBytes - e.resultBytes
				if len(*field) > room {
					*field = (*field)[:room]
					e.resultsTruncated = true
				}
				e.resultBytes += len(*field)
			}
			e.actions = append(e.actions, stored)
		} else {
			e.lastError = fmt.Errorf("action result limit exceeded")
			e.stopScript = true
			e.resultsTruncated = true
		}
		if r.ExitCode != 0 {
			e.failedActions++
		}
	}
	bounded := func(value string) string {
		if len(value) > e.options.MaxVariableBytes {
			e.resultsTruncated = true
			return value[:e.options.MaxVariableBytes]
		}
		return value
	}
	e.vars["host"], e.vars["stdout"], e.vars["stderr"], e.vars["reason"] = bounded(r.Host), bounded(r.Stdout), bounded(r.Stderr), bounded(r.Reason)
	e.vars["exit"] = fmt.Sprint(r.ExitCode)
	e.vars["duration"] = fmt.Sprint(r.DurationMs)
	if r.ExitCode != 0 && e.onError == "stop" {
		e.stopScript = true
	}
	if r.ExitCode != 0 {
		e.hadFailure = true
		if r.Summary && e.lastError == nil {
			e.lastError = fmt.Errorf("%s", r.Reason)
		}
	}
}
func (e *Engine) State() State {
	return State{InHeredoc: e.inHeredoc, Delimiter: e.heredocDelim, BlockDepth: len(e.blocks)}
}
func (e *Engine) Close() error {
	if e.sessionLog != nil {
		err := e.sessionLog.Close()
		e.sessionLog = nil
		return err
	}
	return nil
}
func (e *Engine) printf(format string, args ...any) { fmt.Fprintf(e.options.Output, format, args...) }
func (e *Engine) println(args ...any)               { fmt.Fprintln(e.options.Output, args...) }
func (e *Engine) lookupEnv(key string) string {
	if e.options.LookupEnv != nil {
		return e.options.LookupEnv(key)
	}
	return ""
}
func (e *Engine) loadFile(path string) ([]byte, error) {
	if e.options.LoadFile == nil {
		return nil, fmt.Errorf("script file access is not enabled")
	}
	if err := e.ctx.Err(); err != nil {
		return nil, err
	}
	data, err := e.options.LoadFile(e.ctx, path)
	if err == nil && len(data) > e.options.MaxSourceBytes {
		return nil, fmt.Errorf("script exceeds source byte limit")
	}
	return data, err
}
func (e *Engine) fail(err error) {
	e.mu.Lock()
	e.lastError = err
	e.mu.Unlock()
	e.println("svsh:", err)
	e.RecordResult(ActionResult{Type: "svsh", ExitCode: 1, Reason: err.Error()})
}
func (e *Engine) stopping() bool  { e.mu.Lock(); defer e.mu.Unlock(); return e.stopScript }
func (e *Engine) cancelled() bool { return e.ctx.Err() != nil }
func (e *Engine) wait(duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-e.ctx.Done():
	}
}
func (e *Engine) dispatch(command Command) {
	if e.cancelled() || e.stopping() {
		return
	}
	command.Parameters = e.Variables()
	if err := e.backend.Execute(e.ctx, command, e.RecordResult); err != nil {
		e.fail(err)
	}
}

// Run consumes source without a prompt. Call once on a fresh engine for a job;
// an interactive session may still use ExecLine one line at a time.
func (e *Engine) Run(ctx context.Context, source io.Reader) Result {
	started := time.Now()
	e.ctx = ctx
	e.readSource(source)
	if !e.cancelled() && !e.stopping() && (e.inHeredoc || len(e.blocks) > 0) {
		e.fail(fmt.Errorf("incomplete script: missing HEREDOC delimiter or end"))
	}
	e.mu.Lock()
	result := Result{Status: "success", Actions: append([]ActionResult(nil), e.actions...), TotalActions: e.totalActions, FailedActions: e.failedActions, ActionsTruncated: e.resultsTruncated, StartedAt: started, FinishedAt: time.Now()}
	if e.hadFailure || e.failedActions > 0 || e.lastError != nil {
		result.Status = "failed"
		result.ExitCode = 1
	}
	if e.lastError != nil {
		result.Error = e.lastError.Error()
	}
	e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		result.Status = "cancelled"
		result.ExitCode = 1
		result.Error = err.Error()
	}
	result.Variables = e.Variables()
	return result
}
func (e *Engine) readSource(source io.Reader) {
	e.scriptDepth++
	defer func() { e.scriptDepth-- }()
	reader := bufio.NewReader(io.LimitReader(source, int64(e.options.MaxSourceBytes)+1))
	consumed := 0
	first := true
	for !e.cancelled() && !e.stopping() {
		line, err := reader.ReadString('\n')
		consumed += len(line)
		if consumed > e.options.MaxSourceBytes {
			e.fail(fmt.Errorf("script exceeds source byte limit"))
			break
		}
		if first {
			line = strings.TrimPrefix(line, "\ufeff")
			first = false
		}
		if line != "" {
			if !e.ExecLine(strings.TrimRight(line, "\r\n")) {
				break
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				e.fail(err)
			}
			break
		}
	}
}
