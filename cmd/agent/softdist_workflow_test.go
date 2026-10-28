//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

func TestWorkflowWorkerOrderFailuresAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, failAt, trace, reason string
		code                        int32
	}{
		{"success", "", "REGION=apac\nMAIN\nPOST", "deployment steps completed", 0},
		{"prerequisite refusal", "pre", "", "pre-deployment step 1", 7},
		{"installer failure", "main", "REGION=apac", "main deployment", 8},
		{"post command failure", "post", "REGION=apac\nMAIN", "post-deployment step 1", 9},
		{"tampered zip never starts pre", "hash", "", "bundle preparation failed", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			source := filepath.Join(base, "source")
			if err := os.MkdirAll(filepath.Join(source, "settings"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "settings", "app.ini"), []byte("REGION=apac\r\n"), 0600); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(base, "bundle.zip")
			sum, err := softdist.PackBundle(context.Background(), source, archive, softdist.BundleLimits{})
			if err != nil {
				t.Fatal(err)
			}
			trace := filepath.Join(base, "trace output.txt")
			pre := &pb.SoftwareCommandStep{Name: "check config", Command: "cmd.exe", Arguments: `/D /C type settings\app.ini>>"${trace}"`}
			post := &pb.SoftwareCommandStep{Name: "post action", Command: "cmd.exe", Arguments: `/D /C echo POST>>"${trace}"`}
			spec := &pb.SoftwareDeploySpec{Name: "workflow", InstallerType: "command", FilePath: archive, ExpectedHash: sum, Bundle: &pb.SoftwareBundle{Command: "cmd.exe"}, SilentArgs: `/D /C echo MAIN>>"${trace}"`, Parameters: map[string]string{"trace": trace}, Steps: &pb.SoftwareDeploymentSteps{Before: []*pb.SoftwareCommandStep{pre}, After: []*pb.SoftwareCommandStep{post}}}
			switch tc.failAt {
			case "pre":
				pre.Arguments = "/D /C exit 7"
			case "main":
				spec.SilentArgs = "/D /C exit 8"
			case "post":
				post.Arguments = "/D /C exit 9"
			case "hash":
				spec.ExpectedHash = strings.Repeat("0", 64)
			}
			out := make(chan *pb.AgentMessage, 32)
			installer := newSoftdistInstaller(out, "test", nil, base, filepath.Join(base, "download"), 1, time.Millisecond, nil)
			installer.run(softdistJob{commandID: "workflow-test", spec: spec, localPath: archive})
			var terminal *pb.ActionStatus
			for len(out) > 0 {
				if st := (<-out).GetActionStatus(); st != nil && st.FinishedAt != 0 {
					terminal = st
				}
			}
			if terminal == nil {
				t.Fatal("no terminal result")
			}
			wantPhase := pb.ActionPhase_ACTION_PHASE_FAILED
			if tc.failAt == "" {
				wantPhase = pb.ActionPhase_ACTION_PHASE_SUCCESS
			}
			if terminal.Phase != wantPhase || terminal.ExitCode != tc.code || !strings.Contains(terminal.Reason, tc.reason) {
				t.Fatalf("unexpected result: %+v", terminal)
			}
			data, err := os.ReadFile(trace)
			if tc.trace == "" {
				if !os.IsNotExist(err) {
					t.Fatalf("refused command created trace: %s (%v)", data, err)
				}
			} else if err != nil || strings.TrimSpace(strings.ReplaceAll(string(data), "\r\n", "\n")) != tc.trace {
				t.Fatalf("incorrect process execution order: %q (%v)", data, err)
			}
			if tc.failAt != "hash" {
				if terminal.PackageCleanup != "removed" || terminal.PackageDirectory == "" {
					t.Fatal("workflow lost cleanup")
				}
				if _, err := os.Stat(terminal.PackageDirectory); !os.IsNotExist(err) {
					t.Fatal("extracted package remains")
				}
			}
		})
	}
}

func TestWorkflowCannotBeSentAsOlderTask(t *testing.T) {
	for _, kind := range []string{"software_deploy", "software_deploy_v2", "software_deploy_bundle"} {
		spec := &pb.SoftwareDeploySpec{Bundle: &pb.SoftwareBundle{Command: "cmd.exe"}, Steps: &pb.SoftwareDeploymentSteps{Before: []*pb.SoftwareCommandStep{{Name: "pre", Command: "cmd.exe"}}}}
		payload, _ := proto.Marshal(spec)
		out := make(chan *pb.AgentMessage, 2)
		d := &dispatcher{outbox: out}
		d.handleSoftwareDeployCmd(context.Background(), nil, &pb.Command{Type: kind, Payload: payload})
		st := (<-out).GetActionStatus()
		if st == nil || st.Phase != pb.ActionPhase_ACTION_PHASE_FAILED || !strings.Contains(st.Reason, "software_deploy_workflow") {
			t.Fatal("workflow downgraded or ignored")
		}
	}
}
