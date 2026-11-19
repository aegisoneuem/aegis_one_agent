package svsh

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Job is the same SVSH script execution request for Script Job and Software
// Distribution script steps. Kind identifies the caller; it does not change the
// language or automatically execute an installer. The caller supplies identity,
// authorizes Targets, schedules jobs and persists results.
type Job struct {
	ID             string            `json:"id"`
	Kind           string            `json:"kind"` // script_job, software_distribution, dashboard
	Script         string            `json:"script,omitempty"`
	ScriptFile     string            `json:"script_file,omitempty"`
	Targets        []string          `json:"targets"`
	Parameters     map[string]string `json:"parameters,omitempty"`
	Capture        bool              `json:"capture"`
	MaxOutputBytes int               `json:"max_output_bytes,omitempty"`
	MaxActions     int               `json:"max_actions,omitempty"`
}
type Runner struct {
	// NewBackend MUST create fresh session state and enforce the authorized
	// target boundary, including explicit paths/foreach inside the script.
	NewBackend func(context.Context, Job, io.Writer) (Backend, error)
	LoadFile   func(context.Context, string) ([]byte, error)
	LookupEnv  func(string) string
}
type outputBuffer struct {
	mu        sync.Mutex
	data      []byte
	max       int
	truncated bool
}

func (b *outputBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	room := b.max - len(b.data)
	if len(p) > room {
		b.truncated = true
		p = p[:room]
	}
	b.data = append(b.data, p...)
	return n, nil
}

func (r Runner) Run(ctx context.Context, job Job) Result {
	started := time.Now()
	fail := func(err error) Result {
		status := "failed"
		if ctx.Err() != nil {
			status = "cancelled"
		}
		return Result{ID: job.ID, Kind: job.Kind, Status: status, ExitCode: 1, Error: err.Error(), StartedAt: started, FinishedAt: time.Now()}
	}
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}
	if len(job.Targets) == 0 {
		return fail(fmt.Errorf("job requires explicit authorized targets"))
	}
	if (job.Script == "") == (job.ScriptFile == "") {
		return fail(fmt.Errorf("supply exactly one of script or script_file"))
	}
	if r.NewBackend == nil {
		return fail(fmt.Errorf("SVSH backend factory is required"))
	}
	if len(job.Script) > DefaultMaxSourceBytes {
		return fail(fmt.Errorf("script exceeds source byte limit"))
	}
	maxOut := job.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = 1 << 20
	}
	output := &outputBuffer{max: maxOut}
	backend, err := r.NewBackend(ctx, job, output)
	if err != nil {
		return fail(err)
	}
	if backend == nil {
		return fail(fmt.Errorf("SVSH backend factory returned nil"))
	}
	if err = backend.SelectTargets(ctx, append([]string(nil), job.Targets...)); err != nil {
		return fail(err)
	}
	backend.SetCapture(job.Capture)
	source := job.Script
	if job.ScriptFile != "" {
		if r.LoadFile == nil {
			return fail(fmt.Errorf("script file access is not enabled"))
		}
		data, err := r.LoadFile(ctx, job.ScriptFile)
		if err != nil {
			return fail(err)
		}
		source = string(data)
	}
	engine := New(backend, Options{Output: output, Parameters: job.Parameters, LoadFile: r.LoadFile, LookupEnv: r.LookupEnv, MaxActions: job.MaxActions})
	defer engine.Close()
	result := engine.Run(ctx, strings.NewReader(source))
	result.ID, result.Kind = job.ID, job.Kind
	output.mu.Lock()
	result.Output = string(output.data)
	result.OutputTruncated = output.truncated
	output.mu.Unlock()
	return result
}
