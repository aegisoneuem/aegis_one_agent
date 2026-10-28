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

func TestAdvancedDetectionWorkerLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, action, kind         string
		existing, noOp, secondRule bool
		want                       pb.ActionPhase
	}{
		{"install creates artifact", "install", "file_exists", false, false, false, pb.ActionPhase_ACTION_PHASE_SUCCESS},
		{"already installed skips failing command", "install", "file_exists", true, true, false, pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE},
		{"exit zero without artifact fails verification", "install", "file_exists", false, true, false, pb.ActionPhase_ACTION_PHASE_FAILED},
		{"unversioned output fails version rule", "install", "file_version", false, false, false, pb.ActionPhase_ACTION_PHASE_FAILED},
		{"uninstall unversioned artifact does not skip", "uninstall", "file_version", true, false, false, pb.ActionPhase_ACTION_PHASE_SUCCESS},
		{"uninstall partially present ALL group", "uninstall", "file_exists", true, false, true, pb.ActionPhase_ACTION_PHASE_SUCCESS},
		{"uninstall cannot hide leftover artifact", "uninstall", "file_exists", true, true, true, pb.ActionPhase_ACTION_PHASE_FAILED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			file := filepath.Join(base, "installed output.exe")
			if tc.existing {
				if err := os.WriteFile(file, []byte("old unversioned file"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			rule := &pb.SoftwareDetectionRule{Kind: tc.kind, Path: "${FILE}"}
			if tc.kind == "file_version" {
				rule.MinimumVersion = "1.0"
			}
			spec := &pb.SoftwareDeploySpec{Name: tc.name, Action: tc.action, InstallerType: "command", FilePath: "cmd.exe", SilentArgs: `/D /C echo installed>"${FILE}"`, Parameters: map[string]string{"FILE": file}, Detection: &pb.SoftwareDetection{Mode: "all", Rules: []*pb.SoftwareDetectionRule{rule}}}
			if tc.action == "uninstall" {
				spec.SilentArgs = `/D /C del /Q "${FILE}"`
			}
			if tc.noOp {
				spec.SilentArgs = "/D /C exit 0"
				if tc.want == pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE {
					spec.SilentArgs = "/D /C exit 7"
				}
			}
			if tc.secondRule {
				spec.Detection.Rules = append(spec.Detection.Rules, &pb.SoftwareDetectionRule{Kind: "file_exists", Path: filepath.Join(base, "missing.txt")})
			}
			out := make(chan *pb.AgentMessage, 16)
			installer := newSoftdistInstaller(out, "test", nil, base, base, 1, time.Millisecond, nil)
			installer.run(softdistJob{commandID: "detect-test", spec: spec})
			var terminal *pb.ActionStatus
			for len(out) > 0 {
				if st := (<-out).GetActionStatus(); st != nil && st.FinishedAt != 0 {
					terminal = st
				}
			}
			if terminal == nil || terminal.Phase != tc.want {
				t.Fatalf("unexpected lifecycle result: %+v", terminal)
			}
			if tc.want == pb.ActionPhase_ACTION_PHASE_FAILED && !strings.Contains(terminal.Reason, "verification failed") {
				t.Fatal(terminal)
			}
		})
	}
}

func TestAdvancedDetectionRefusesOlderSignedTaskTypes(t *testing.T) {
	for _, kind := range []string{"software_deploy", "software_deploy_v2", "software_deploy_bundle", "software_deploy_workflow"} {
		spec := &pb.SoftwareDeploySpec{Detection: &pb.SoftwareDetection{Rules: []*pb.SoftwareDetectionRule{{Kind: "file_exists", Path: `C:\app\file`}}}}
		raw, _ := proto.Marshal(spec)
		out := make(chan *pb.AgentMessage, 2)
		d := &dispatcher{outbox: out}
		d.handleSoftwareDeployCmd(context.Background(), nil, &pb.Command{Type: kind, Payload: raw})
		st := (<-out).GetActionStatus()
		if st == nil || st.Phase != pb.ActionPhase_ACTION_PHASE_FAILED || !strings.Contains(st.Reason, "software_deploy_detect") {
			t.Fatal("custom detection was ignored or downgraded", st)
		}
	}
}

func TestAdvancedDetectionKeepsBundleAndWorkflowSettings(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "config.txt"), []byte("config"), 0600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(base, "package.zip")
	sum, err := softdist.PackBundle(context.Background(), source, archive, softdist.BundleLimits{})
	if err != nil {
		t.Fatal(err)
	}
	spec := &pb.SoftwareDeploySpec{InstallerType: "command", FilePath: archive, ExpectedHash: sum, Bundle: &pb.SoftwareBundle{Command: "cmd.exe"}, Steps: &pb.SoftwareDeploymentSteps{Before: []*pb.SoftwareCommandStep{{Name: "precheck", Command: "cmd.exe", Arguments: "/D /C exit 0"}}}, Detection: &pb.SoftwareDetection{Rules: []*pb.SoftwareDetectionRule{{Kind: "file_exists", Path: filepath.Join(base, "installed.txt")}}}}
	raw, _ := proto.Marshal(spec)
	out := make(chan *pb.AgentMessage, 8)
	installer := newSoftdistInstaller(out, "test", nil, base, base, 1, time.Millisecond, nil)
	d := &dispatcher{outbox: out, softdist: installer}
	d.handleSoftwareDeployCmd(context.Background(), nil, &pb.Command{Type: "software_deploy_detect", CommandId: "combined-test", Payload: raw})
	if len(installer.jobs) != 1 {
		for len(out) > 0 {
			t.Log(<-out)
		}
		t.Fatal("combined detection/bundle/workflow task was refused")
	}
	job := <-installer.jobs
	if job.spec.Detection == nil || job.spec.Bundle == nil || job.spec.Steps == nil {
		t.Fatal("one required feature was silently dropped")
	}
}
