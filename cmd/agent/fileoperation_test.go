package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	"github.com/aegisoneuem/aegis_one_agent/internal/fileops"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type fileRelayTestServer struct{ captureUploadServer }

func (s *fileRelayTestServer) FetchContent(_ *pb.ContentRequest, stream pb.AgentControlPlane_FetchContentServer) error {
	for offset := 0; offset < len(s.data); offset += 12345 {
		end := offset + 12345
		if end > len(s.data) {
			end = len(s.data)
		}
		if err := stream.Send(&pb.ContentChunk{Data: s.data[offset:end]}); err != nil {
			return err
		}
	}
	return nil
}
func TestNativeFileTaskCrossEndpointRelayAndLocalMove(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source with spaces.bin")
	data := bytes.Repeat([]byte{0, 255, 1, 13, 10}, 250000)
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(2 << 20)
	server := grpc.NewServer()
	relay := &fileRelayTestServer{}
	pb.RegisterAgentControlPlaneServer(server, relay)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///file-relay", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	client := pb.NewAgentControlPlaneClient(conn)
	sourceAgent := &dispatcher{cfg: config.Config{SecureDir: filepath.Join(root, "source-agent")}}
	destAgent := &dispatcher{cfg: config.Config{SecureDir: filepath.Join(root, "dest-agent")}}
	packed, err := sourceAgent.runFileOperation(context.Background(), client, "source-task", fileops.Spec{Operation: "pack", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "destination", "renamed.bin")
	_, err = destAgent.runFileOperation(context.Background(), client, "dest-task", fileops.Spec{Operation: "unpack", Destination: dest, ContentID: "bound-id", SHA256: packed.SHA256, Entries: packed.Entries})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("relayed data differs")
	}
	moved := filepath.Join(root, "moved.bin")
	_, err = sourceAgent.runFileOperation(context.Background(), nil, "move-task", fileops.Spec{Operation: "move", Source: source, Destination: moved})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(source); !os.IsNotExist(err) {
		t.Fatal("move source still exists")
	}
	got, _ = os.ReadFile(moved)
	if !bytes.Equal(got, data) {
		t.Fatal("local move changed bytes")
	}
}
