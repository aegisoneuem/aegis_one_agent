package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/proto"
)

// nth returns the i-th trimmed element of list. A single-element list applies to
// all indices (so one KB/HASH can cover several files); otherwise out of range
// returns "".
func nth(list []string, i int) string {
	if len(list) == 1 {
		return strings.TrimSpace(list[0])
	}
	if i < len(list) {
		return strings.TrimSpace(list[i])
	}
	return ""
}

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

		// Optionally push one or more signed install_patch commands. Set
		// AEGIS_INSTALL_FILE (and optionally _KB / _HASH / _HASH_ALGO); each may be
		// a COMMA-SEPARATED list (same index across lists) to simulate a multi-patch
		// job — the agent queues them and installs one at a time. The file path is
		// LOCAL to the agent machine; the server never streams bytes here.
		if filesEnv := os.Getenv("AEGIS_INSTALL_FILE"); filesEnv != "" {
			files := strings.Split(filesEnv, ",")
			kbs := strings.Split(os.Getenv("AEGIS_INSTALL_KB"), ",")
			hashes := strings.Split(os.Getenv("AEGIS_INSTALL_HASH"), ",")
			algo := os.Getenv("AEGIS_INSTALL_HASH_ALGO")
			for i, f := range files {
				f = strings.TrimSpace(f)
				if f == "" {
					continue
				}
				spec := &pb.InstallPatchSpec{
					Kb:            nth(kbs, i),
					FilePath:      f,
					ExpectedHash:  nth(hashes, i),
					HashAlgorithm: algo,
				}
				specBytes, err := proto.Marshal(spec)
				if err != nil {
					log.Printf("failed to marshal install spec: %v", err)
					continue
				}
				cmdID := fmt.Sprintf("install-%03d", i+1)
				installCmd := &pb.ServerMessage{
					Payload: &pb.ServerMessage_Command{
						Command: &pb.Command{
							CommandId: cmdID,
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
				fmt.Printf("Sent command -> id: %s, type: install_patch, kb=%s, file=%s (signed)\n", cmdID, spec.Kb, f)
			}
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
		if st := msg.GetInstallStatus(); st != nil {
			fmt.Printf("Received install status -> command_id: %s, kb: %s, phase: %s, exit_code: %d, reboot_required: %v, reason: %s, detail: %s\n",
				st.CommandId, st.Kb, st.Phase, st.ExitCode, st.RebootRequired, st.Reason, st.Detail)
		}
		if ds := msg.GetDeviceState(); ds != nil {
			fmt.Printf("Received device-state (Layer 1) -> device: %s, product: %s, build: %s, arch: %s, installed_kbs: %d\n",
				ds.DeviceId, ds.ProductName, ds.Build, ds.Architecture, len(ds.InstalledKbs))
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

	// mTLS: require and verify a client cert. Cert paths via env (defaults suit a
	// local `certs` dir created by cmd/certgen).
	caFile := getenv("AEGIS_TLS_CA", "certs/ca.crt")
	certFile := getenv("AEGIS_TLS_CERT", "certs/server.crt")
	keyFile := getenv("AEGIS_TLS_KEY", "certs/server.key")
	creds, err := secure.ServerCredentials(caFile, certFile, keyFile)
	if err != nil {
		log.Fatalf("mTLS setup failed (run certgen first?): %v", err)
	}

	// Allow the agent's keepalive pings (client pings ~60s; without a permissive
	// policy the server would GOAWAY with "too_many_pings"). The real server needs
	// an equivalent policy.
	s := grpc.NewServer(
		grpc.Creds(creds),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             20 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	pb.RegisterAgentControlPlaneServer(s, &server{})
	fmt.Printf("Mock server listening on :50051 (mTLS: ca=%s server=%s)\n", caFile, certFile)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
