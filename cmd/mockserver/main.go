package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
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

		// Optionally push a signed install_patch command. Set AEGIS_INSTALL_FILE
		// (and optionally _KB / _HASH / _HASH_ALGO) to simulate the server
		// delivering a patch file and telling the agent to install it. The file
		// path is LOCAL to the agent machine — the server never streams bytes here.
		if file := os.Getenv("AEGIS_INSTALL_FILE"); file != "" {
			spec := &pb.InstallPatchSpec{
				Kb:            os.Getenv("AEGIS_INSTALL_KB"),
				FilePath:      file,
				ExpectedHash:  os.Getenv("AEGIS_INSTALL_HASH"),
				HashAlgorithm: os.Getenv("AEGIS_INSTALL_HASH_ALGO"),
			}
			specBytes, err := proto.Marshal(spec)
			if err != nil {
				log.Printf("failed to marshal install spec: %v", err)
				return
			}
			installCmd := &pb.ServerMessage{
				Payload: &pb.ServerMessage_Command{
					Command: &pb.Command{
						CommandId: "install-001",
						Type:      "install_patch",
						Payload:   specBytes,
						Signature: signPayload(specBytes),
					},
				},
			}
			if err := stream.Send(installCmd); err != nil {
				log.Printf("failed to send install command: %v", err)
				return
			}
			fmt.Printf("Sent command -> id: install-001, type: install_patch, kb=%s, file=%s (signed)\n", spec.Kb, file)
		}
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
		if ir := msg.GetInstallResult(); ir != nil {
			fmt.Printf("Received install result -> command_id: %s, kb: %s, success: %v, reboot_required: %v, exit_code: %d\n    detail: %s\n",
				ir.CommandId, ir.Kb, ir.Success, ir.RebootRequired, ir.ExitCode, ir.Detail)
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
