package main

import (
	"testing"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

func TestKnownOfflineDeviceSelectableWithoutHostsOrHeartbeat(t *testing.T) {
	srv := &server{byID: map[string]*deviceRecord{"dev-1": {DeviceID: "dev-1", Hostname: "offline-vm", ComputerID: "1234567890"}}, liveAgents: make(map[string]*liveAgent), pending: make(map[string]chan *pb.ActionStatus)}
	s := &nshSession{srv: srv}
	s.verbCD(`\\offline-vm`)
	if len(s.targets) != 1 || s.targets[0] != "dev-1" {
		t.Fatalf("offline selection=%v", s.targets)
	}
	st, _ := s.sendCommand("dev-1", "hostname", "")
	if st != nil || len(srv.pending) != 0 {
		t.Fatal("offline command was queued or waited for a reply")
	}
	if id, err := srv.resolveKnownTarget("1234567890"); err != nil || id != "dev-1" {
		t.Fatalf("computer-id lookup: %q %v", id, err)
	}
}

func TestAmbiguousHostnameDoesNotChangeSelection(t *testing.T) {
	srv := &server{byID: map[string]*deviceRecord{"device-a": {DeviceID: "device-a", Hostname: "duplicate"}, "device-b": {DeviceID: "device-b", Hostname: "duplicate"}}}
	s := &nshSession{srv: srv, targets: []string{"device-a"}}
	s.verbCD(`\\duplicate`)
	if len(s.targets) != 1 || s.targets[0] != "device-a" {
		t.Fatal("ambiguous name changed target")
	}
	if id, err := srv.resolveKnownTarget("device-b"); err != nil || id != "device-b" {
		t.Fatalf("exact ID=%q %v", id, err)
	}
	if _, err := srv.resolveKnownTarget("device-"); err == nil {
		t.Fatal("ambiguous prefix accepted")
	}
}
