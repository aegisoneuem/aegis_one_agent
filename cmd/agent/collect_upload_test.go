package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type captureUploadServer struct {
	pb.UnimplementedAgentControlPlaneServer
	data []byte
}

func (s *captureUploadServer) UploadContent(stream pb.AgentControlPlane_UploadContentServer) error {
	s.data = nil
	var digest string
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		s.data = append(s.data, chunk.Data...)
		if chunk.Sha256Full != "" {
			digest = chunk.Sha256Full
		}
	}
	h := sha256.Sum256(s.data)
	return stream.SendAndClose(&pb.UploadAck{StoredPath: "saved.bin", ReceivedBytes: int64(len(s.data)), IntegrityOk: hex.EncodeToString(h[:]) == digest})
}

// Exercise the real upload stream: zero means uncapped stdout, not zero bytes.
func TestUploadFilePreservesBytesAndHonorsPositiveCap(t *testing.T) {
	listener := bufconn.Listen(2 << 20)
	server := grpc.NewServer()
	receiver := &captureUploadServer{}
	pb.RegisterAgentControlPlaneServer(server, receiver)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///upload-test", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	client := pb.NewAgentControlPlaneClient(conn)
	data := bytes.Repeat([]byte{0, 255, '\r', '\n', 42}, 250000)
	path := filepath.Join(t.TempDir(), "with spaces.bin")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, cap := range []int64{0, 23} {
		n, _, err := uploadFile(context.Background(), client, "test", "file.bin", path, 0, cap)
		if err != nil {
			t.Fatal(err)
		}
		want := data
		if cap > 0 {
			want = data[len(data)-int(cap):]
		}
		if n != int64(len(want)) || !bytes.Equal(receiver.data, want) {
			t.Fatalf("cap=%d: sent %d bytes, wanted %d exact bytes", cap, n, len(want))
		}
	}
}
