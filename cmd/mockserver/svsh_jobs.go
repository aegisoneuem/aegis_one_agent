package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/pkg/svsh"
)

func newMockSVSHRunner(srv *server) svsh.Runner {
	return svsh.Runner{LoadFile: localSVSHFile, LookupEnv: os.Getenv, NewBackend: func(ctx context.Context, job svsh.Job, output io.Writer) (svsh.Backend, error) {
		allowed := make(map[string]bool, len(job.Targets))
		for _, id := range job.Targets {
			srv.mu.Lock()
			_, known := srv.byID[id]
			srv.mu.Unlock()
			if !known {
				return nil, fmt.Errorf("unknown target device_id: %s", id)
			}
			allowed[id] = true
		}
		s := &svshSession{srv: srv, ctx: ctx, shell: "cmd", timeout: 300, maxOutput: 1 << 20, outputDir: "svsh/jobs/" + slugify(job.ID), output: output, errorOutput: output, allowedTargets: allowed, jobMode: true}
		return &mockSVSHBackend{s: s}, nil
	}}
}
func (s *svshSession) verbJob(rest string) error {
	args, err := splitSVSHArgs(rest)
	if err != nil {
		return err
	}
	if len(args) < 2 || args[0] != "run" {
		return fmt.Errorf("usage: job run <file.svsh> [--param NAME=value] [--kind script_job|software_distribution] [--timeout seconds] [--capture]")
	}
	job := svsh.Job{ID: "svsh-job-" + svshRandomID(), Kind: "script_job", ScriptFile: args[1], Targets: append([]string(nil), s.targets...), Parameters: map[string]string{}}
	timeout := 1800
	for i := 2; i < len(args); i++ {
		if args[i] == "--capture" {
			job.Capture = true
			continue
		}
		if i+1 >= len(args) {
			return fmt.Errorf("%s needs a value", args[i])
		}
		key, value := args[i], args[i+1]
		i++
		switch key {
		case "--param":
			parts := strings.SplitN(value, "=", 2)
			if len(parts) != 2 || parts[0] == "" {
				return fmt.Errorf("--param expects NAME=value")
			}
			job.Parameters[parts[0]] = parts[1]
		case "--kind":
			if value != "script_job" && value != "software_distribution" && value != "dashboard" {
				return fmt.Errorf("unsupported job kind %s", value)
			}
			job.Kind = value
		case "--timeout":
			timeout, err = strconv.Atoi(value)
			if err != nil || timeout <= 0 {
				return fmt.Errorf("timeout needs positive seconds")
			}
		default:
			return fmt.Errorf("unknown job option %s", key)
		}
	}
	if len(job.Targets) == 0 {
		return fmt.Errorf("select target(s) with cd before running a job")
	}
	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeout)*time.Second)
	defer cancel()
	result := newMockSVSHRunner(s.srv).Run(ctx, job)
	s.print(result.Output)
	s.printf("---- JOB %s %s: actions=%d failed=%d\n", result.ID, strings.ToUpper(result.Status), result.TotalActions, result.FailedActions)
	if result.Error != "" {
		s.println("     reason:", result.Error)
	}
	if path, err := writeSVSHJobResult(result); err != nil {
		return err
	} else {
		s.println("     result:", path)
	}
	s.recordAction(svsh.ActionResult{Type: "svsh_job", ActionID: job.ID, ExitCode: result.ExitCode, Reason: result.Status, Summary: true})
	return nil
}
func writeSVSHJobResult(result svsh.Result) (string, error) {
	path := filepath.Join("logs", "svsh-jobs", result.ID+".json")
	if override := strings.TrimSpace(os.Getenv("AEGIS_SVSH_JOB_RESULT")); override != "" {
		path = override
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0600)
}
func runEnvironmentSVSHJob(srv *server, path string) {
	s := &svshSession{srv: srv}
	var ids []string
	for _, token := range splitCSV(os.Getenv("AEGIS_SVSH_TARGETS")) {
		id, err := srv.resolveKnownTarget(token)
		if err != nil {
			s.println("svsh job:", err)
			return
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		srv.liveMu.Lock()
		for id := range srv.liveAgents {
			ids = append(ids, id)
		}
		srv.liveMu.Unlock()
	}
	job := svsh.Job{ID: "svsh-job-" + svshRandomID(), Kind: getenv("AEGIS_SVSH_JOB_KIND", "script_job"), ScriptFile: path, Targets: ids, Parameters: parseParams(os.Getenv("AEGIS_SVSH_JOB_PARAMS")), Capture: truthy(getenv("AEGIS_SVSH_JOB_CAPTURE", "true"))}
	seconds, _ := strconv.Atoi(getenv("AEGIS_SVSH_JOB_TIMEOUT_SECONDS", "1800"))
	if seconds <= 0 {
		s.println("svsh job: timeout must be positive")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
	defer cancel()
	result := newMockSVSHRunner(srv).Run(ctx, job)
	s.print(result.Output)
	s.printf("---- JOB %s %s\n", job.ID, strings.ToUpper(result.Status))
	if result.Error != "" {
		s.println("     reason:", result.Error)
	}
	if path, err := writeSVSHJobResult(result); err != nil {
		s.println("svsh job result:", err)
	} else {
		s.println("     result:", path)
	}
}
