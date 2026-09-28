package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"time"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/grpc"
)

const privateKeyB64 = "+KSVnrEXSXNsyCL0FVZ0TLOB28gKyL0L7KRxzjtUAXqyW5fNX8seQt4BH7WV2GUBn6EGh+QlaAKtUCRWJxgs+A=="

func signPayload(payload []byte) []byte {

	privBytes, err := base64.StdEncoding.DecodeString(privateKeyB64)
	if err != nil {
		log.Fatalf("invalid private key: %v", err)
	}
	priv := ed25519.PrivateKey(privBytes)
	return ed25519.Sign(priv, payload)
}

type server struct {
	pb.UnimplementedAgentControlPlaneServer
}

func (s *server) Connect(stream pb.AgentControlPlane_ConnectServer) error {
	fmt.Println("Agent connected!")

	go func() {
		time.Sleep(2 * time.Second)
		payload := []byte("")
		command := &pb.ServerMessage{
			Payload: &pb.ServerMessage_Command{
				Command: &pb.Command{
					CommandId: "cmd-001",
					Type:      "hostname",
					Payload:   payload,
					Signature: signPayload(payload),
					//Signature: signPayload([]byte("wrong-payload")),
				},
			},
		}
		if err := stream.Send(command); err != nil {
			log.Printf("failed to send command: %v", err)
			return
		}
		fmt.Println("Sent command -> id: cmd-001, type: hostname (signed)")
	}()

	for {
		msg, err := stream.Recv()
		if err != nil {
			fmt.Println("Agent disconnected:", err)
			return err
		}
		if hb := msg.GetHeartbeat(); hb != nil {
			fmt.Printf("Received heartbeat -> device: %s, version: %s, os: %s\n",
				hb.DeviceId, hb.AgentVersion, hb.Os)
		}
		if result := msg.GetCommandResult(); result != nil {
			fmt.Printf("Received command result -> command_id: %s, status: %s, message: %s\n",
				result.CommandId, result.Status, result.Message)
		}
		if psr := msg.GetPatchScanResult(); psr != nil {
			fmt.Printf("Received patch scan result -> device: %s, build: %s, arch: %s, source: %s, missing: %d\n",
				psr.DeviceId, psr.Build, psr.Architecture, psr.Source, len(psr.Missing))
			for _, m := range psr.Missing {
				fmt.Printf("    - [%s] %s (KB=%v, severity=%s, %.1fMB, reboot=%v, cves=%v)\n",
					m.Classification, m.Title, m.KbArticleIds, m.MsrcSeverity, m.SizeMb, m.RebootRequired, m.CveIds)
			}
		}
	}
}

func main() {
	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	s := grpc.NewServer()
	pb.RegisterAgentControlPlaneServer(s, &server{})
	fmt.Println("Mock server listening on :50051")
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
