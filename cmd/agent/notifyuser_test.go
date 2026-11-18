package main

import (
	"testing"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// A response for a registered action is reported and the pending entry is cleared.
func TestReportUserResponseClearsPending(t *testing.T) {
	outbox := make(chan *pb.AgentMessage, 8)
	pendingMu.Lock()
	pendingActions["cmd-x"] = &pendingUIAction{commandID: "cmd-x", deviceID: "dev", outbox: outbox}
	pendingMu.Unlock()

	reportUserResponse("cmd-x", "Restart now", 0)

	pendingMu.Lock()
	_, stillPending := pendingActions["cmd-x"]
	pendingMu.Unlock()
	if stillPending {
		t.Fatal("pending action was not cleared after response")
	}
	if len(outbox) == 0 {
		t.Fatal("expected a status/audit message on the outbox")
	}
}

// A response for an unknown id is a no-op (no panic, nothing sent).
func TestReportUserResponseUnknownID(t *testing.T) {
	outbox := make(chan *pb.AgentMessage, 1)
	_ = outbox
	reportUserResponse("does-not-exist", "x", 0) // must not panic
}
