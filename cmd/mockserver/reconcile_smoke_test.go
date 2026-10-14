package main

import (
	"testing"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

func TestReconcileAssignsRandomComputerID(t *testing.T) {
	s := &server{
		byID:           make(map[string]*deviceRecord),
		bySysUUID:      make(map[string]*deviceRecord),
		byTPM:          make(map[string]*deviceRecord),
		computerIDUsed: make(map[string]struct{}),
	}
	// New device.
	a, _, rej := s.reconcile(&pb.EnrollRequest{DeviceId: "dev-1", TpmEk: "tpm:aaa", SystemUuid: "uuid-1"})
	if rej != "" || a != "dev-1" {
		t.Fatalf("assigned=%q rej=%q", a, rej)
	}
	cid := s.computerIDFor("dev-1")
	if len(cid) != 10 || cid[0] == '0' {
		t.Fatalf("computer_id should be 10 digits no leading zero, got %q", cid)
	}
	// Bare record (connect before enroll) then enroll → must still get an id.
	s.byID["dev-2"] = &deviceRecord{DeviceID: "dev-2"} // no ComputerID
	s.reconcile(&pb.EnrollRequest{DeviceId: "dev-2"})
	if c := s.computerIDFor("dev-2"); len(c) != 10 {
		t.Fatalf("bare-record enroll should assign a 10-digit id, got %q", c)
	}
	// Re-enroll same device → SAME computer id (idempotent).
	s.reconcile(&pb.EnrollRequest{DeviceId: "dev-1", TpmEk: "tpm:aaa"})
	if c := s.computerIDFor("dev-1"); c != cid {
		t.Fatalf("re-enroll changed computer_id %q -> %q", cid, c)
	}
}
