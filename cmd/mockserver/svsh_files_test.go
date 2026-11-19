//go:build windows

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aegisoneuem/aegis_one_agent/internal/fileops"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type fileEndpointStream struct {
	pb.AgentControlPlane_ConnectServer
	server              *server
	id, root, artifacts string
	mu                  sync.Mutex
	operations          []string
}

func (s *fileEndpointStream) Send(msg *pb.ServerMessage) error {
	cmd := msg.GetCommand()
	var spec fileops.Spec
	if err := json.Unmarshal(cmd.Payload, &spec); err != nil {
		return err
	}
	if spec.WorkingDir == "" {
		spec.WorkingDir = s.root
	}
	s.mu.Lock()
	s.operations = append(s.operations, spec.Operation)
	s.mu.Unlock()
	var result fileops.Result
	var err error
	switch spec.Operation {
	case "pack":
		jobDir := filepath.Join(s.artifacts, cmd.CommandId)
		if err := os.MkdirAll(jobDir, 0700); err != nil {
			return err
		}
		archive := filepath.Join(jobDir, "bundle.zip")
		result, err = fileops.Pack(context.Background(), spec, archive)
		if err == nil {
			s.server.pendingMu.Lock()
			if s.server.fileReceipts == nil {
				s.server.fileReceipts = make(map[string]fileReceipt)
			}
			s.server.fileReceipts[cmd.CommandId] = fileReceipt{path: archive, sha256: result.SHA256, deviceID: s.id}
			s.server.pendingMu.Unlock()
		}
	case "unpack":
		s.server.pendingMu.Lock()
		content := s.server.fileContent[spec.ContentID]
		s.server.pendingMu.Unlock()
		if content.deviceID != s.id || content.sha256 != spec.SHA256 {
			err = fmt.Errorf("unauthorized destination")
		} else {
			result, err = fileops.Unpack(context.Background(), spec, content.path)
		}
	default:
		result, err = fileops.Basic(context.Background(), spec)
	}
	phase := pb.ActionPhase_ACTION_PHASE_SUCCESS
	reason := "completed"
	exit := int32(0)
	if err != nil {
		phase = pb.ActionPhase_ACTION_PHASE_FAILED
		reason = err.Error()
		exit = 1
	}
	detail, _ := json.Marshal(result)
	s.server.deliverActionStatus(&pb.ActionStatus{ActionId: cmd.CommandId, Phase: phase, Reason: reason, ExitCode: exit, Detail: string(detail)})
	return nil
}
func TestFileCopyOneToManyAndBulkBasics(t *testing.T) {
	srv := &server{byID: make(map[string]*deviceRecord), liveAgents: make(map[string]*liveAgent), pending: make(map[string]chan *pb.ActionStatus)}
	roots := map[string]string{}
	streams := map[string]*fileEndpointStream{}
	for _, id := range []string{"source", "dest-a", "dest-b"} {
		root := t.TempDir()
		roots[id] = root
		stream := &fileEndpointStream{server: srv, id: id, root: root, artifacts: t.TempDir()}
		streams[id] = stream
		srv.byID[id] = &deviceRecord{DeviceID: id, Hostname: id}
		srv.liveAgents[id] = &liveAgent{deviceID: id, stream: stream}
	}
	data := []byte{0, 255, 1, 2, 13, 10}
	if err := os.WriteFile(filepath.Join(roots["source"], "data.bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	sess := &svshSession{srv: srv, targets: []string{"source"}, timeout: 20}
	sess.engine = newScriptEngine(sess)
	sess.engine.execLine(`copy data.bin \\dest-a,dest-b\received\`)
	if sess.engine.vars["exit"] != "0" {
		t.Fatal("copy failed")
	}
	for _, id := range []string{"dest-a", "dest-b"} {
		got, err := os.ReadFile(filepath.Join(roots[id], "received", "data.bin"))
		if err != nil || string(got) != string(data) {
			t.Fatalf("%s wrong data: %v", id, err)
		}
	}
	if len(streams["source"].operations) != 1 {
		t.Fatal("source exported more than once for distribution")
	}
	if len(srv.fileContent) != 0 || len(srv.fileReceipts) != 0 {
		t.Fatal("transfer authorization/receipt leaked after completion")
	}
	sess.engine.execLine(`mkdir \\dest-a,dest-b\new-folder`)
	sess.engine.execLine(`touch \\dest-a,dest-b\new-folder\empty.txt`)
	sess.engine.execLine(`delete \\dest-a,dest-b\new-folder\empty.txt`)
	for _, id := range []string{"dest-a", "dest-b"} {
		if _, err := os.Stat(filepath.Join(roots[id], "new-folder", "empty.txt")); !os.IsNotExist(err) {
			t.Fatal("bulk delete failed")
		}
	}
	if _, err := os.Stat(filepath.Join(roots["source"], "data.bin")); err != nil {
		t.Fatal("copy deleted source")
	}
	srv.byID["offline"] = &deviceRecord{DeviceID: "offline", Hostname: "offline"}
	sess.engine.execLine(`move data.bin \\offline\moved.bin`)
	if _, err := os.Stat(filepath.Join(roots["source"], "data.bin")); err != nil || sess.engine.vars["exit"] != "1" {
		t.Fatal("failed destination move removed source or hid failure")
	}
	sess.engine.execLine(`move data.bin \\dest-b\moved.bin`)
	if _, err := os.Stat(filepath.Join(roots["source"], "data.bin")); !os.IsNotExist(err) {
		t.Fatal("verified cross-host move did not remove source")
	}
	if got, err := os.ReadFile(filepath.Join(roots["dest-b"], "moved.bin")); err != nil || string(got) != string(data) {
		t.Fatal("cross-host move destination differs")
	}
}

type contentTestStream struct {
	pb.AgentControlPlane_FetchContentServer
	ctx  context.Context
	data []byte
}

func (s *contentTestStream) Context() context.Context { return s.ctx }
func (s *contentTestStream) Send(chunk *pb.ContentChunk) error {
	s.data = append(s.data, chunk.Data...)
	return nil
}
func TestFileRelayIsBoundToDestinationAndDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bundle")
	os.WriteFile(path, []byte("bytes"), 0600)
	srv := &server{fileContent: map[string]fileContent{"token": {path: path, deviceID: "allowed", sha256: "digest"}}}
	for _, tc := range []struct {
		id, digest string
		ok         bool
	}{{"other", "digest", false}, {"allowed", "wrong", false}, {"allowed", "digest", true}} {
		cert := &x509.Certificate{Subject: pkix.Name{CommonName: tc.id}}
		ctx := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}}})
		stream := &contentTestStream{ctx: ctx}
		err := srv.FetchContent(&pb.ContentRequest{Kind: "svsh-file", Id: "token", Sha256: tc.digest}, stream)
		if tc.ok {
			if err != nil || string(stream.data) != "bytes" {
				t.Fatalf("authorized fetch: %s %v", stream.data, err)
			}
		} else if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("unauthorized fetch err=%v", err)
		}
	}
}
