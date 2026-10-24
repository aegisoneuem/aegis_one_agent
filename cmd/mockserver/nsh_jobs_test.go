package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/pkg/nsh"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

type jobTestStream struct {
	pb.AgentControlPlane_ConnectServer
	server  *server
	id      string
	noReply bool
	mu      sync.Mutex
	calls   int
}

func (s *jobTestStream) Send(msg *pb.ServerMessage) error {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.noReply {
		return nil
	}
	cmd := msg.GetCommand()
	var spec pb.RunCommandSpec
	if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
		return err
	}
	out := fmt.Sprintf("%s:%s:%s", s.id, spec.Command, spec.Parameters["TAG"])
	detail, _ := json.Marshal(map[string]any{"stdout": out, "duration_ms": 1})
	s.server.deliverActionStatus(&pb.ActionStatus{ActionId: cmd.CommandId, ActionType: "run_command", Phase: pb.ActionPhase_ACTION_PHASE_SUCCESS, Detail: string(detail), Reason: "completed"})
	return nil
}
func jobTestServer() (*server, map[string]*jobTestStream) {
	srv := &server{byID: make(map[string]*deviceRecord), liveAgents: make(map[string]*liveAgent), pending: make(map[string]chan *pb.ActionStatus)}
	streams := map[string]*jobTestStream{}
	for _, id := range []string{"a", "b"} {
		srv.byID[id] = &deviceRecord{DeviceID: id, Hostname: "host-" + id}
		stream := &jobTestStream{server: srv, id: id}
		streams[id] = stream
		srv.liveAgents[id] = &liveAgent{deviceID: id, stream: stream}
	}
	return srv, streams
}
func TestSharedMockRunnerSupportsBothJobKindsAndScope(t *testing.T) {
	srv, streams := jobTestServer()
	for _, kind := range []string{"script_job", "software_distribution"} {
		result := newMockNSHRunner(srv).Run(context.Background(), nsh.Job{ID: kind, Kind: kind, Targets: []string{"a"}, Parameters: map[string]string{"TAG": kind}, Script: "foreach host in *\necho ${TAG}\nend\n"})
		if result.Status != "success" || result.TotalActions != 1 || len(result.Actions) != 1 || result.Actions[0].TargetID != "a" || !strings.Contains(result.Output, kind) {
			t.Fatalf("job: %+v", result)
		}
	}
	if streams["b"].calls != 0 {
		t.Fatal("job wildcard escaped authorized targets")
	}
	result := newMockNSHRunner(srv).Run(context.Background(), nsh.Job{Targets: []string{"a"}, Script: "cd \\\\host-b\nhostname\n"})
	if result.Status != "failed" || streams["b"].calls != 0 {
		t.Fatal("explicit cd escaped job scope")
	}
	result = newMockNSHRunner(srv).Run(context.Background(), nsh.Job{Targets: []string{"a"}, Script: "hosts\n"})
	if strings.Contains(result.Output, "host-b") {
		t.Fatal("inventory leaked outside job scope")
	}
}
func TestMockJobCancellationClearsPendingWait(t *testing.T) {
	srv, streams := jobTestServer()
	streams["a"].noReply = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result := newMockNSHRunner(srv).Run(ctx, nsh.Job{Targets: []string{"a"}, Script: "hostname"})
	if result.Status != "cancelled" {
		t.Fatalf("%+v", result)
	}
	srv.pendingMu.Lock()
	pending := len(srv.pending)
	srv.pendingMu.Unlock()
	if pending != 0 {
		t.Fatal("cancelled job leaked pending wait")
	}
}

func TestJobWildcardReportsScopedOfflineTargetAsFailure(t *testing.T) {
	srv, _ := jobTestServer()
	delete(srv.liveAgents, "a")
	result := newMockNSHRunner(srv).Run(context.Background(), nsh.Job{Targets: []string{"a"}, Script: "foreach host in *\nhostname\nend\n"})
	if result.Status != "failed" || !strings.Contains(result.Output, "offline") {
		t.Fatalf("offline target silently skipped: %+v", result)
	}
}
func TestJobRunCommandPersistsStructuredResultWithoutChangingSession(t *testing.T) {
	srv, _ := jobTestServer()
	dir := t.TempDir()
	path := filepath.Join(dir, "job.nsh")
	os.WriteFile(path, []byte("echo ${TAG}"), 0600)
	resultPath := filepath.Join(dir, "result.json")
	t.Setenv("AEGIS_NSH_JOB_RESULT", resultPath)
	s := &nshSession{srv: srv, targets: []string{"a"}, shell: "powershell", timeout: 40, capture: true}
	if err := s.verbJob(`run "` + path + `" --param TAG=job-tag --kind software_distribution`); err != nil {
		t.Fatal(err)
	}
	var result nsh.Result
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "success" || result.Kind != "software_distribution" || !strings.Contains(result.Output, "job-tag") {
		t.Fatalf("%+v", result)
	}
	if s.shell != "powershell" || !s.capture || len(s.targets) != 1 || s.targets[0] != "a" {
		t.Fatal("job altered interactive state")
	}
}
