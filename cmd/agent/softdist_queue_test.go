//go:build windows

package main

import (
	"strings"
	"testing"
	"time"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

func TestSoftdistReportsWaitingBeforeRunning(t *testing.T) {
	out := make(chan *pb.AgentMessage, 16)
	base := t.TempDir()
	s := newSoftdistInstaller(out, "test-device", nil, base, base, 1, time.Millisecond, nil, 0)
	job := softdistJob{commandID: "gate-wait-test", spec: &pb.SoftwareDeploySpec{
		Name: "Gate wait", Action: "install", InstallerType: "command",
		FilePath: "cmd.exe", SilentArgs: "/D /C exit 7", TimeoutSeconds: 5,
	}}
	machineInstallGate.Lock()
	locked := true
	defer func() {
		if locked {
			machineInstallGate.Unlock()
		}
	}()
	done := make(chan struct{})
	go func() { defer close(done); s.run(job) }()
	select {
	case msg := <-out:
		st := msg.GetActionStatus()
		if st == nil || st.Phase != pb.ActionPhase_ACTION_PHASE_QUEUED || !strings.Contains(st.Reason, "waiting for another") {
			t.Fatalf("expected explicit queued wait, got %v", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no wait status while install gate was held")
	}
	select {
	case msg := <-out:
		t.Fatalf("action advanced before gate release: %v", msg.GetActionStatus())
	default:
	}
	machineInstallGate.Unlock()
	locked = false
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("worker did not finish after gate release")
	}
	st := (<-out).GetActionStatus()
	if st == nil || st.Phase != pb.ActionPhase_ACTION_PHASE_RUNNING {
		t.Fatalf("expected running after gate release, got %v", st)
	}
}
