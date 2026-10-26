package main

import (
	"context"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestLegacyDeployRejectsPrivateFields(t *testing.T) {
	for _, spec := range []*pb.SoftwareDeploySpec{
		{Execution: &pb.SoftwareExecution{Mode: "logged_in_user"}},
		{SecureParameters: map[string]string{"TOKEN": "private-test"}},
	} {
		payload, err := proto.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		out := make(chan *pb.AgentMessage, 2)
		d := &dispatcher{outbox: out} // No installer: any fallthrough would panic.
		d.handleSoftwareDeployCmd(context.Background(), nil, &pb.Command{Type: "software_deploy", CommandId: "private-legacy", Payload: payload})
		if len(out) != 1 {
			t.Fatal("expected terminal rejection")
		}
		status := (<-out).GetActionStatus()
		if status == nil || status.Phase != pb.ActionPhase_ACTION_PHASE_FAILED {
			t.Fatal("missing failed status")
		}
		for _, b := range payload {
			if b != 0 {
				t.Fatal("private raw payload not cleared")
			}
		}
	}
}
func TestPasswordCannotBePublicParameter(t *testing.T) {
	spec := &pb.SoftwareDeploySpec{Execution: &pb.SoftwareExecution{Mode: "specified_account", Username: "bob", PasswordUtf8: []byte("account-canary")}, Parameters: map[string]string{"public": "account-canary"}}
	if validatePrivateDeployment(spec, 0) == nil {
		t.Fatal("account password accepted as public parameter")
	}
	spec.Parameters = nil
	if err := validatePrivateDeployment(spec, 0); err != nil {
		t.Fatal(err)
	}
	if validatePrivateDeployment(spec, 2) == nil {
		t.Fatal("oversized password accepted")
	}
	clearPrivateDeployment(spec)
	for _, b := range spec.Execution.PasswordUtf8 {
		if b != 0 {
			t.Fatal("password buffer retained")
		}
	}
}
