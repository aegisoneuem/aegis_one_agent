package main

import (
	"fmt"
	"testing"
)

func TestHostSearchIsBoundedForFiftyThousandDevices(t *testing.T) {
	srv := &server{byID: make(map[string]*deviceRecord), liveAgents: make(map[string]*liveAgent)}
	for i := 0; i < 50000; i++ {
		id := fmt.Sprintf("device-%05d", i)
		srv.byID[id] = &deviceRecord{DeviceID: id, Hostname: fmt.Sprintf("vm-%05d", i), ComputerID: fmt.Sprintf("%010d", i+1000000000)}
		if i%2 == 0 {
			srv.liveAgents[id] = nil
		}
	}
	q, err := parseHostQuery("")
	if err != nil {
		t.Fatal(err)
	}
	rows, total := srv.hostPage(q)
	if len(rows) != 20 || total != 50000 {
		t.Fatalf("rendered %d rows of %d", len(rows), total)
	}
	q, err = parseHostQuery("host=vm-000 --status offline --page 2 --limit 10")
	if err != nil {
		t.Fatal(err)
	}
	rows, total = srv.hostPage(q)
	if len(rows) != 10 || total != 50 || rows[0].host != "vm-00021" || rows[0].status != "OFFLINE" {
		t.Fatalf("filtered page wrong: %v total=%d", rows, total)
	}
	for _, bad := range []string{"--limit 50000", "--page -1", "--status maybe", "--page"} {
		if _, err := parseHostQuery(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
func TestNSHDevicePathsAndQuotedArguments(t *testing.T) {
	srv := &server{byID: map[string]*deviceRecord{"a": {Hostname: "srv1"}, "b": {Hostname: "vm2"}}, liveAgents: map[string]*liveAgent{"a": {}, "b": {}}}
	s := &nshSession{srv: srv}
	paths, err := s.endpointPaths(`\\srv1,vm2\c\temp\`)
	if err != nil || len(paths) != 2 || paths[0].path != `c:\temp\` || !paths[0].directory {
		t.Fatalf("%+v %v", paths, err)
	}
	args, err := splitNSHArgs(`"\\srv1\c\temp\with spaces.bin" "\\vm2\d$\new folder\"`)
	if err != nil || len(args) != 2 || args[1] != `\\vm2\d$\new folder\` {
		t.Fatalf("%q %v", args, err)
	}
}
