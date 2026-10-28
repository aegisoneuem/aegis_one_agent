//go:build windows

package main

import (
	"context"
	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBundleWorkerRelativeFilesAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, typ    string
		retain, fail bool
	}{
		{"command managed", "command", false, false}, {"command custom retain", "command", true, false},
		{"failed managed command", "command", false, true}, {"batch entry with support file", "bat", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			source := filepath.Join(base, "source")
			os.MkdirAll(filepath.Join(source, "settings"), 0700)
			os.WriteFile(filepath.Join(source, "settings", "config.ini"), []byte("REGION=apac\r\n"), 0600)
			output := filepath.Join(base, "Installed Files", "config.ini")
			os.MkdirAll(filepath.Dir(output), 0700)
			os.WriteFile(filepath.Join(source, "install.cmd"), []byte("@echo off\r\ncopy /Y settings\\config.ini \"%~1\" >nul\r\nexit /b %errorlevel%\r\n"), 0600)
			archive := filepath.Join(base, "package.zip")
			sum, err := softdist.PackBundle(context.Background(), source, archive, softdist.BundleLimits{})
			if err != nil {
				t.Fatal(err)
			}
			bundle := &pb.SoftwareBundle{Command: "cmd.exe"}
			args := `/D /C copy /Y settings\config.ini "` + output + `"`
			if tc.fail {
				args = "/D /C exit 7"
			}
			if tc.typ == "bat" {
				bundle.Command = ""
				bundle.EntryPoint = "install.cmd"
				args = `"` + output + `"`
			}
			if tc.retain {
				bundle.Destination = filepath.Join(base, "Retained Package")
			}
			spec := &pb.SoftwareDeploySpec{Name: "Bundle worker test", Action: "install", InstallerType: tc.typ, FilePath: archive, ExpectedHash: sum, SilentArgs: args, Bundle: bundle, TimeoutSeconds: 10}
			out := make(chan *pb.AgentMessage, 16)
			installer := newSoftdistInstaller(out, "test-device", nil, base, filepath.Join(base, "download"), 1, time.Millisecond, nil)
			// Direct worker test uses the current test identity. Service/WTS token
			// acquisition is separate VM acceptance; public dispatch normalizes SYSTEM.
			installer.run(softdistJob{commandID: "bundle-test", spec: spec, localPath: archive})
			var terminal *pb.ActionStatus
			for len(out) > 0 {
				if st := (<-out).GetActionStatus(); st != nil && st.FinishedAt != 0 {
					terminal = st
				}
			}
			if terminal == nil {
				t.Fatal("no terminal status")
			}
			if tc.fail {
				if terminal.Phase != pb.ActionPhase_ACTION_PHASE_FAILED || terminal.ExitCode != 7 {
					t.Fatalf("bad failure: %+v", terminal)
				}
			} else {
				if terminal.Phase != pb.ActionPhase_ACTION_PHASE_SUCCESS {
					t.Fatalf("bundle execution failed: %+v", terminal)
				}
				data, err := os.ReadFile(output)
				if err != nil || !strings.Contains(string(data), "REGION=apac") {
					t.Fatal("relative supporting file was not used")
				}
			}
			if terminal.PackageDirectory == "" {
				t.Fatal("package path missing from result")
			}
			_, err = os.Stat(terminal.PackageDirectory)
			if tc.retain {
				if terminal.PackageCleanup != "retained" || err != nil {
					t.Fatal("custom bundle not retained")
				}
			} else if terminal.PackageCleanup != "removed" || !os.IsNotExist(err) {
				t.Fatal("managed bundle cleanup failed")
			}
			if _, err = os.Stat(archive); err != nil {
				t.Fatal("unowned pre-staged ZIP was deleted")
			}
		})
	}
}
func TestBundleRequiresDistinctSignedTask(t *testing.T) {
	for _, kind := range []string{"software_deploy", "software_deploy_v2"} {
		spec := &pb.SoftwareDeploySpec{Bundle: &pb.SoftwareBundle{Command: "cmd.exe"}}
		payload, _ := proto.Marshal(spec)
		out := make(chan *pb.AgentMessage, 2)
		d := &dispatcher{outbox: out}
		d.handleSoftwareDeployCmd(context.Background(), nil, &pb.Command{Type: kind, Payload: payload})
		if status := (<-out).GetActionStatus(); status == nil || status.Phase != pb.ActionPhase_ACTION_PHASE_FAILED {
			t.Fatal("bundle downgraded to an older task")
		}
	}
}
func TestCommandBundleCannotBypassZIPIntegrity(t *testing.T) {
	out := make(chan *pb.AgentMessage, 2)
	installer := newSoftdistInstaller(out, "test", nil, t.TempDir(), t.TempDir(), 1, time.Millisecond, nil)
	spec := &pb.SoftwareDeploySpec{InstallerType: "command", FilePath: "package.zip", Bundle: &pb.SoftwareBundle{Command: "cmd.exe"}}
	installer.deliver(context.Background(), nil, "bundle-unpinned", spec)
	if len(installer.jobs) != 0 {
		t.Fatal("command bundle bypassed file delivery hash gate")
	}
	if status := (<-out).GetActionStatus(); status.Phase != pb.ActionPhase_ACTION_PHASE_FAILED {
		t.Fatal("unverified bundle not refused")
	}
}
