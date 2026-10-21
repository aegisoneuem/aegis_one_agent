//go:build !windows

package main

import (
	"context"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// runShellSession stub for non-Windows builds (Linux CI). The agent only runs on
// Windows; this exists so the whole package still compiles on linux/amd64 for
// vet and tests.
func runShellSession(_ context.Context, _ pb.AgentControlPlaneClient, outbox chan<- *pb.AgentMessage, deviceID, _ /*secureDir*/, cmdID string, _ *pb.OpenShellSpec) {
	finishShell(outbox, deviceID, cmdID, pb.ActionPhase_ACTION_PHASE_FAILED, "open_shell is Windows-only")
}

func finishShell(outbox chan<- *pb.AgentMessage, deviceID, cmdID string, phase pb.ActionPhase, reason string) {
	st := simpleActionStatus(cmdID, actionOpenShell, deviceID, "", phase, reason)
	outbox <- &pb.AgentMessage{Payload: &pb.AgentMessage_ActionStatus{ActionStatus: st}}
}
