package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
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
	caCert    *x509.Certificate // CA used to sign enrolled client certs (the "tiny vault")
	caKey     any               // CA private key (ecdsa/ed25519/rsa)
	caCertPEM []byte            // CA cert PEM returned in the enroll response chain

	mu        sync.Mutex
	byID      map[string]*deviceRecord // device registry keyed by assigned device_id
	bySysUUID map[string]*deviceRecord // secondary index for dedup (reinstall/reimage)
}

// deviceRecord is one registered device (the real server persists this in a DB;
// the mock keeps it in memory). hostname is a mutable attribute, never the key.
type deviceRecord struct {
	DeviceID    string
	SystemUUID  string
	MachineGUID string
	Hostname    string
	LastSeen    time.Time
}

// reconcile resolves an enrollment to a single logical device, de-duplicating by
// device_id then by SMBIOS system_uuid (so a reinstall/reimage with a fresh
// device_id maps back to the existing record). Returns the assigned device_id and
// whether this was a dedup (reconciled to a different id than requested).
func (s *server) reconcile(req *pb.EnrollRequest) (assigned string, deduped bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case req.DeviceId != "" && s.byID[req.DeviceId] != nil:
		assigned = req.DeviceId // known device_id → same record
	case req.SystemUuid != "" && s.bySysUUID[req.SystemUuid] != nil:
		assigned = s.bySysUUID[req.SystemUuid].DeviceID // same hardware, new id → dedup
		deduped = assigned != req.DeviceId
	default:
		assigned = req.DeviceId // brand-new device
	}

	rec := &deviceRecord{
		DeviceID:    assigned,
		SystemUUID:  req.SystemUuid,
		MachineGUID: req.MachineGuid,
		Hostname:    req.Hostname,
		LastSeen:    time.Now(),
	}
	s.byID[assigned] = rec
	if req.SystemUuid != "" {
		s.bySysUUID[req.SystemUuid] = rec
	}
	return assigned, deduped
}

// Enroll is the tiny-vault reference: it validates the bootstrap token, signs the
// agent's CSR with the CA, and returns a per-device client cert. The real server
// also enforces licensing + device reconciliation here (see
// docs/device-identity-and-lifecycle.md).
func (s *server) Enroll(_ context.Context, req *pb.EnrollRequest) (*pb.EnrollResponse, error) {
	want := strings.TrimSpace(os.Getenv("AEGIS_ENROLL_TOKEN"))
	if want == "" || req.EnrollToken != want {
		fmt.Printf("Enroll REJECTED -> device_id=%s (bad/missing token)\n", req.DeviceId)
		return &pb.EnrollResponse{Error: "invalid enrollment token"}, nil
	}
	block, _ := pem.Decode([]byte(req.CsrPem))
	if block == nil {
		return &pb.EnrollResponse{Error: "bad CSR PEM"}, nil
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return &pb.EnrollResponse{Error: "parse CSR: " + err.Error()}, nil
	}
	if err := csr.CheckSignature(); err != nil {
		return &pb.EnrollResponse{Error: "CSR signature invalid"}, nil
	}
	// Reconcile to a single logical device (dedup by device_id then system_uuid).
	assigned, deduped := s.reconcile(req)

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return &pb.EnrollResponse{Error: "serial: " + err.Error()}, nil
	}
	// The cert identity is the ASSIGNED id (not the CSR's CN) — so a reconciled
	// device gets a cert bound to its canonical id.
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkixName(assigned),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().AddDate(1, 0, 0), // 1-year cert (real server rotates)
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, s.caCert, csr.PublicKey, s.caKey)
	if err != nil {
		return &pb.EnrollResponse{Error: "sign cert: " + err.Error()}, nil
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if deduped {
		fmt.Printf("Enroll OK (RECONCILED/dedup) -> requested=%s assigned=%s system_uuid=%s hostname=%s (same hardware → existing device)\n",
			req.DeviceId, assigned, req.SystemUuid, req.Hostname)
	} else {
		fmt.Printf("Enroll OK -> device_id=%s system_uuid=%s machine_guid=%s hostname=%s os=%s (cert signed)\n",
			assigned, req.SystemUuid, req.MachineGuid, req.Hostname, req.Os)
	}
	return &pb.EnrollResponse{
		ClientCertPem:    string(certPEM),
		CaChainPem:       string(s.caCertPEM),
		AssignedDeviceId: assigned,
	}, nil
}

func pkixName(cn string) pkix.Name { return pkix.Name{CommonName: cn} }

// loadCA loads the CA cert + private key the vault signs enrollments with.
func loadCA(certPath, keyPath string) (*x509.Certificate, any, []byte, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read CA cert: %w", err)
	}
	cblock, _ := pem.Decode(certPEM)
	if cblock == nil {
		return nil, nil, nil, fmt.Errorf("bad CA cert PEM")
	}
	caCert, err := x509.ParseCertificate(cblock.Bytes)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse CA cert: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read CA key: %w", err)
	}
	kblock, _ := pem.Decode(keyPEM)
	if kblock == nil {
		return nil, nil, nil, fmt.Errorf("bad CA key PEM")
	}
	key, err := parsePrivateKey(kblock.Bytes)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse CA key: %w", err)
	}
	return caCert, key, certPEM, nil
}

func parsePrivateKey(der []byte) (any, error) {
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		return k, nil
	}
	return x509.ParsePKCS1PrivateKey(der)
}

// FetchContent streams a file to the agent (simulated peer-cache delivery). The
// file to serve comes from AEGIS_SERVE_CAB (kind=cab) or AEGIS_SERVE_PATCH
// (kind=patch). This is the server contract the real peer-cache must implement.
// clientVerified reports whether the peer presented a client cert that verified
// against the CA. On the single port, Enroll is allowed without one (token-gated)
// but all other RPCs require a verified client cert.
func clientVerified(ctx context.Context) bool {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return false
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return false
	}
	return len(tlsInfo.State.VerifiedChains) > 0
}

func (s *server) FetchContent(req *pb.ContentRequest, stream pb.AgentControlPlane_FetchContentServer) error {
	if !clientVerified(stream.Context()) {
		return status.Error(codes.Unauthenticated, "client certificate required")
	}
	var path string
	switch req.Kind {
	case "cab":
		path = os.Getenv("AEGIS_SERVE_CAB")
	case "patch":
		path = os.Getenv("AEGIS_SERVE_PATCH")
	}
	if path == "" {
		return fmt.Errorf("no file configured to serve for kind %q (set AEGIS_SERVE_CAB/_PATCH)", req.Kind)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Printf("FetchContent: serving %s (kind=%s, id=%s)\n", path, req.Kind, req.Id)
	buf := make([]byte, 64*1024)
	var total int64
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if serr := stream.Send(&pb.ContentChunk{Data: buf[:n]}); serr != nil {
				return serr
			}
			total += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	fmt.Printf("FetchContent: done (%d bytes)\n", total)
	return nil
}

func (s *server) Connect(stream pb.AgentControlPlane_ConnectServer) error {
	if !clientVerified(stream.Context()) {
		fmt.Println("Connect REJECTED -> no verified client certificate (must enroll first)")
		return status.Error(codes.Unauthenticated, "client certificate required")
	}
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

		// Optionally push one or more signed stage_patch commands (pre-cache ahead
		// of a scheduled install). Set AEGIS_STAGE_KB (and optionally _HASH /
		// _HASH_ALGO / _FILE as a filename hint for the extension); comma-separated
		// for multiple. The agent FETCHES the bytes via FetchContent, so the server
		// must also serve them: set AEGIS_SERVE_PATCH to the file to stream.
		if kbEnv := os.Getenv("AEGIS_STAGE_KB"); kbEnv != "" {
			kbs := strings.Split(kbEnv, ",")
			hashes := strings.Split(os.Getenv("AEGIS_STAGE_HASH"), ",")
			files := strings.Split(os.Getenv("AEGIS_STAGE_FILE"), ",")
			algo := os.Getenv("AEGIS_STAGE_HASH_ALGO")
			for i, kb := range kbs {
				kb = strings.TrimSpace(kb)
				if kb == "" {
					continue
				}
				spec := &pb.InstallPatchSpec{
					Kb:            kb,
					FilePath:      nth(files, i), // filename hint only (for the .msu/.cab extension)
					ExpectedHash:  nth(hashes, i),
					HashAlgorithm: algo,
				}
				specBytes, err := proto.Marshal(spec)
				if err != nil {
					log.Printf("failed to marshal stage spec: %v", err)
					continue
				}
				cmdID := fmt.Sprintf("stage-%03d", i+1)
				stageCmd := &pb.ServerMessage{Payload: &pb.ServerMessage_Command{Command: &pb.Command{
					CommandId: cmdID,
					Type:      "stage_patch",
					Payload:   specBytes,
					Signature: signPayload(specBytes),
				}}}
				if err := stream.Send(stageCmd); err != nil {
					log.Printf("failed to send stage command: %v", err)
					return
				}
				fmt.Printf("Sent command -> id: %s, type: stage_patch, kb=%s (signed)\n", cmdID, kb)
			}
		}

		// Optionally push a patch baseline for the agent's offline self-diff. Set
		// AEGIS_BASELINE_BUILD (the device build) and AEGIS_BASELINE_KBS
		// (comma-separated expected KBs) to simulate the server's snapshot.
		if build := os.Getenv("AEGIS_BASELINE_BUILD"); build != "" {
			kbs := strings.Split(os.Getenv("AEGIS_BASELINE_KBS"), ",")
			for i := range kbs {
				kbs[i] = strings.TrimSpace(kbs[i])
			}
			bl := &pb.ServerMessage{Payload: &pb.ServerMessage_PatchBaseline{
				PatchBaseline: &pb.PatchBaseline{Build: build, LatestKbs: kbs, IssuedAt: time.Now().Unix()},
			}}
			if err := stream.Send(bl); err != nil {
				log.Printf("failed to send baseline: %v", err)
				return
			}
			fmt.Printf("Sent patch baseline -> build=%s, latest_kbs=%v\n", build, kbs)
		}

		// Optionally push the current cab version. Set AEGIS_CAB_VERSION (and
		// optionally AEGIS_CAB_SHA256) to simulate the server advertising a cab.
		if ver := os.Getenv("AEGIS_CAB_VERSION"); ver != "" {
			cm := &pb.ServerMessage{Payload: &pb.ServerMessage_CabManifest{
				CabManifest: &pb.CabManifest{Version: ver, Sha256: os.Getenv("AEGIS_CAB_SHA256")},
			}}
			if err := stream.Send(cm); err != nil {
				log.Printf("failed to send cab manifest: %v", err)
				return
			}
			fmt.Printf("Sent cab manifest -> version=%s\n", ver)
		}

		// Optionally send a signed reboot command. Set AEGIS_REBOOT_DELAY (seconds)
		// to schedule a reboot (use a large value + then test cancel_reboot, or
		// revert the VM snapshot).
		if rd := os.Getenv("AEGIS_REBOOT_DELAY"); rd != "" {
			delay, _ := strconv.Atoi(rd)
			spec := &pb.RebootSpec{DelaySeconds: int32(delay), Message: "Aegis One: scheduled reboot for patching"}
			b, _ := proto.Marshal(spec)
			reboot := &pb.ServerMessage{Payload: &pb.ServerMessage_Command{Command: &pb.Command{
				CommandId: "reboot-001", Type: "reboot", Payload: b, Signature: signPayload(b),
			}}}
			if err := stream.Send(reboot); err != nil {
				log.Printf("failed to send reboot command: %v", err)
				return
			}
			fmt.Printf("Sent command -> id: reboot-001, type: reboot, delay=%ds (signed)\n", delay)
		}

		// Optionally send a signed cancel_reboot command (set AEGIS_CANCEL_REBOOT=1).
		if os.Getenv("AEGIS_CANCEL_REBOOT") != "" {
			payload := []byte("")
			cancel := &pb.ServerMessage{Payload: &pb.ServerMessage_Command{Command: &pb.Command{
				CommandId: "cancel-reboot-001", Type: "cancel_reboot", Payload: payload, Signature: signPayload(payload),
			}}}
			if err := stream.Send(cancel); err != nil {
				log.Printf("failed to send cancel_reboot: %v", err)
				return
			}
			fmt.Println("Sent command -> id: cancel-reboot-001, type: cancel_reboot (signed)")
		}

		// Parallel-run demo: while a (long) install runs on the agent's serial
		// worker, fire periodic commands that execute on the receiver — proving the
		// agent handles a concurrent task alongside a patch install. Set
		// AEGIS_PARALLEL_PINGS=N to send N hostname commands at ~8s intervals.
		if n := os.Getenv("AEGIS_PARALLEL_PINGS"); n != "" {
			count, _ := strconv.Atoi(n)
			for i := 1; i <= count; i++ {
				time.Sleep(8 * time.Second)
				payload := []byte("")
				ping := &pb.ServerMessage{Payload: &pb.ServerMessage_Command{Command: &pb.Command{
					CommandId: fmt.Sprintf("ping-%03d", i),
					Type:      "hostname",
					Payload:   payload,
					Signature: signPayload(payload),
				}}}
				if err := stream.Send(ping); err != nil {
					log.Printf("failed to send parallel ping: %v", err)
					return
				}
				fmt.Printf("Sent parallel ping -> id: ping-%03d, type: hostname (while install may be running)\n", i)
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
			fmt.Printf("Received heartbeat -> device: %s, hostname: %s, version: %s, os: %s\n",
				hb.DeviceId, hb.Hostname, hb.AgentVersion, hb.Os)
			// Attribute update from heartbeat: hostname is mutable and NOT the key.
			// A rename keeps the same device record — only the attribute changes.
			s.mu.Lock()
			rec := s.byID[hb.DeviceId]
			if rec == nil {
				s.byID[hb.DeviceId] = &deviceRecord{DeviceID: hb.DeviceId, Hostname: hb.Hostname, LastSeen: time.Now()}
				fmt.Printf("  device %s seen via heartbeat (registered), hostname: %s\n", hb.DeviceId, hb.Hostname)
			} else if hb.Hostname != "" && rec.Hostname != hb.Hostname {
				fmt.Printf("  device %s RENAMED: %q -> %q (same record — hostname is just an attribute)\n", hb.DeviceId, rec.Hostname, hb.Hostname)
				rec.Hostname = hb.Hostname
			}
			s.mu.Unlock()
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
		if cf := msg.GetCacheFull(); cf != nil {
			fmt.Printf("Received CACHE-FULL report -> device: %s, pending: %s, needed: %d, cap: %d, in_use: %d, msg: %s\n",
				cf.DeviceId, cf.PendingId, cf.NeededBytes, cf.CapBytes, cf.InUseBytes, cf.Message)
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
	// Single port: verify a client cert WHEN presented (ops) but allow none
	// (enrollment, token-gated). Non-Enroll RPCs require a verified cert at the
	// app layer (see Connect / FetchContent).
	creds, err := secure.ServerCredentialsVerifyIfGiven(caFile, certFile, keyFile)
	if err != nil {
		log.Fatalf("TLS setup failed (run certgen first?): %v", err)
	}

	// Allow the agent's keepalive pings (client pings ~60s; without a permissive
	// policy the server would GOAWAY with "too_many_pings"). The real server needs
	// an equivalent policy.
	// The tiny-vault reference: load the CA so Enroll can sign agent CSRs.
	caKeyFile := getenv("AEGIS_CA_KEY", "certs/ca.key")
	caCert, caKey, caCertPEM, err := loadCA(caFile, caKeyFile)
	if err != nil {
		log.Fatalf("CA load failed (needed for enrollment): %v", err)
	}
	srv := &server{
		caCert: caCert, caKey: caKey, caCertPEM: caCertPEM,
		byID:      make(map[string]*deviceRecord),
		bySysUUID: make(map[string]*deviceRecord),
	}

	s := grpc.NewServer(
		grpc.Creds(creds),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             20 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	pb.RegisterAgentControlPlaneServer(s, srv)
	fmt.Printf("Mock server listening on :50051 (single port: mTLS ops + enrollment; token=%s)\n", maskToken(os.Getenv("AEGIS_ENROLL_TOKEN")))
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}

func maskToken(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return "<unset — enrollment will reject>"
	}
	if len(t) <= 4 {
		return "****"
	}
	return t[:2] + "****" + t[len(t)-2:]
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
