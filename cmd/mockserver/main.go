package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/policy"
	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	"github.com/aegisoneuem/aegis_one_proto/commandsign"
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

func loadSignKey() ed25519.PrivateKey {
	privBytes, err := base64.StdEncoding.DecodeString(privateKeyB64)
	if err != nil {
		mockFatalf("invalid private key: %v", err)
	}
	return ed25519.PrivateKey(privBytes)
}

// sign fills cmd.Signature over the canonical (type + command_id + payload)
// bytes and returns cmd, so each call site wraps its literal: sign(&pb.Command{...}).
// Reading type/id/payload back off the struct means the signed envelope can
// never drift from what is actually sent. The agent verifies with the same
// commandsign package.
func sign(cmd *pb.Command) *pb.Command {
	// Backfill attribution so the agent always sees a value. Dispatchers
	// that know who the operator is (NSH REPL, console UI) populate Actor
	// / Source BEFORE calling sign; this fallback covers env-var-driven
	// mock dispatches, scheduled auto-tasks, and tests.
	if strings.TrimSpace(cmd.Actor) == "" {
		cmd.Actor = defaultDispatchActor()
	}
	if strings.TrimSpace(cmd.Source) == "" {
		cmd.Source = "mockserver.server"
	}
	if cmd.IssuedAt == 0 {
		cmd.IssuedAt = time.Now().Unix()
	}
	cmd.Signature = commandsign.Sign(loadSignKey(), cmd.Type, cmd.CommandId, cmd.Payload)
	return cmd
}

// defaultDispatchActor returns the attribution string used when no caller
// has set Command.Actor. Format: "server:auto[<os-user>@<host>]" so an
// auditor sees which mock process ran the command even for an auto-
// triggered dispatch (env-vars, test harness).
func defaultDispatchActor() string {
	user := os.Getenv("USERNAME")
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" {
		user = "unknown"
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "mockserver"
	}
	return "server:auto[" + user + "@" + host + "]"
}

type server struct {
	pb.UnimplementedAgentControlPlaneServer
	caCert    *x509.Certificate // CA used to sign enrolled client certs (the "tiny vault")
	caKey     any               // CA private key (ecdsa/ed25519/rsa)
	caCertPEM []byte            // CA cert PEM returned in the enroll response chain

	mu        sync.Mutex
	statePath string                   // JSON file the registry persists to (DB stand-in)
	byID      map[string]*deviceRecord // device registry keyed by assigned device_id
	bySysUUID map[string]*deviceRecord // secondary index for dedup (reinstall/reimage)
	byTPM     map[string]*deviceRecord // strongest dedup index: TPM EK (reinstall/format/clone-proof)
	// computerIDUsed tracks assigned console ids so a fresh RANDOM 10-digit id never
	// collides. Ids are random (not sequential) so a customer cannot infer how many
	// devices have enrolled, and no device looks like "the first computer".
	computerIDUsed map[string]struct{}

	// shellPending tracks open_shell commands we have sent that have not yet been
	// dialed back by the agent's ShellSession RPC. Keyed by session_id; a dial
	// consumes the entry (single-use), so a replayed session_id is refused.
	shellMu      sync.Mutex
	shellPending map[string]*shellSession

	// liveAgents is the connected-agent registry the NSH REPL targets. Keyed by
	// device_id (the cert CN); carries the Connect stream so the REPL can push
	// signed commands directly. Register on Connect, deregister on return.
	liveMu     sync.Mutex
	liveAgents map[string]*liveAgent

	// pending correlates signed command_ids with the REPL goroutines waiting on
	// their terminal ActionStatus. The ActionStatus receive loop forwards a
	// terminal phase to the channel and closes it; non-REPL (regular) traffic
	// prints normally.
	pendingMu     sync.Mutex
	pending       map[string]chan *pb.ActionStatus
	nshUploads    map[string]nshUploadTarget // protected by pendingMu
	fileReceipts  map[string]fileReceipt     // protected by pendingMu
	fileContent   map[string]fileContent     // protected by pendingMu; destination-bound
	planPending   map[string]*planWait       // protected by pendingMu; device-bound terminal results
	planContent   map[string]fileContent     // protected by pendingMu; device/content key
	planAttempted map[string]bool            // one configured startup plan per device/process; no reconnect replay

	// lastInventory caches the most recent DeviceStateReport per device so the
	// NSH `inventory apps` verb can answer ad-hoc operator queries about which
	// third-party apps the endpoint reported (and at which versions). The server
	// team's real catalog match runs server-side; this cache is a dev/testing
	// convenience, not a durable store.
	inventoryMu       sync.Mutex
	lastInventory     map[string]*pb.DeviceStateReport
	// lastFullInventory caches the most recent Tier-3 FullInventoryReport
	// per device (HW + SW snapshot sent every 6-12 h or on reconnect).
	// Mock-side convenience for NSH queries about the HW inventory.
	lastFullInventory map[string]*pb.FullInventoryReport

}

// liveAgent is one currently-connected agent, discoverable by the REPL.
type liveAgent struct {
	sendMu   sync.Mutex
	deviceID string
	stream   pb.AgentControlPlane_ConnectServer
	since    time.Time
}

// shellSession is a parked remote-shell session the agent is expected to dial.
type shellSession struct {
	started time.Time
}

// deviceRecord is one registered device (the real server persists this in a DB;
// the mock keeps it in memory). hostname is a mutable attribute, never the key.
type deviceRecord struct {
	DeviceID    string    `json:"device_id"`
	SystemUUID  string    `json:"system_uuid,omitempty"`
	MachineGUID string    `json:"machine_guid,omitempty"`
	TpmEK       string    `json:"tpm_ek,omitempty"`
	ComputerID  string    `json:"computer_id"` // server-assigned numeric console id
	Hostname    string    `json:"hostname,omitempty"`
	LastSeen    time.Time `json:"last_seen"`
}

// loadRegistry restores the device registry from the persistence file. The REAL
// server keeps this in a DB; the mock used to keep it only in memory, so every
// restart lost it — a reinstalled agent then failed to dedup and was issued a NEW
// device_id + computer_id. Persisting fixes exactly that (dedup survives restart).
func (s *server) loadRegistry() {
	if s.statePath == "" {
		return
	}
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		return // no state yet — first run
	}
	var recs []*deviceRecord
	if err := json.Unmarshal(data, &recs); err != nil {
		log.Printf("mock state: could not parse %s: %v", s.statePath, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range recs {
		if r.DeviceID == "" {
			continue
		}
		s.byID[r.DeviceID] = r
		if r.SystemUUID != "" {
			s.bySysUUID[r.SystemUUID] = r
		}
		if r.TpmEK != "" {
			s.byTPM[r.TpmEK] = r
		}
		if r.ComputerID != "" {
			s.computerIDUsed[r.ComputerID] = struct{}{}
		}
	}
	mockPrintf("mock state: loaded %d device(s) from %s (dedup survives restart)\n", len(recs), s.statePath)
}

// saveLocked persists the whole registry to disk atomically. Caller holds s.mu.
// byID holds every record (the other maps are secondary indexes onto the same
// records), so iterating it captures the full set.
func (s *server) saveLocked() {
	if s.statePath == "" {
		return
	}
	recs := make([]*deviceRecord, 0, len(s.byID))
	for _, r := range s.byID {
		recs = append(recs, r)
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return
	}
	tmp := s.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Printf("mock state: could not write %s: %v", s.statePath, err)
		return
	}
	if err := os.Rename(tmp, s.statePath); err != nil {
		log.Printf("mock state: could not install %s: %v", s.statePath, err)
	}
}

// reconcile resolves an enrollment to a single logical device, de-duplicating by
// device_id then by SMBIOS system_uuid (so a reinstall/reimage with a fresh
// device_id maps back to the existing record), and enforces the license cap.
// Returns the assigned device_id, whether this was a dedup, and a non-empty
// rejection reason if a NEW device would exceed the license (existing/dedup
// devices never consume a license and are always allowed).
func (s *server) reconcile(req *pb.EnrollRequest) (assigned string, deduped bool, rejected string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	isNew := false
	var existing *deviceRecord
	// Dedup order, strongest hardware signal first: TPM EK → device_id → SMBIOS UUID.
	// TPM EK survives reinstall/format and is clone-proof, so it reconciles a formatted
	// machine back to the same logical device even when device_id + SMBIOS changed.
	switch {
	case req.TpmEk != "" && s.byTPM[req.TpmEk] != nil:
		existing = s.byTPM[req.TpmEk]
	case req.DeviceId != "" && s.byID[req.DeviceId] != nil:
		existing = s.byID[req.DeviceId]
	case req.SystemUuid != "" && s.bySysUUID[req.SystemUuid] != nil:
		existing = s.bySysUUID[req.SystemUuid]
	}
	if existing != nil {
		assigned = existing.DeviceID // same hardware → existing record
		deduped = assigned != req.DeviceId
	} else {
		assigned = req.DeviceId // brand-new device
		isNew = true
	}

	// License: only a NEW device consumes a seat. Re-enroll/reinstall/dedup of an
	// already-registered device does not.
	if isNew {
		if max := licenseMax(); max > 0 && len(s.byID) >= max {
			return "", false, fmt.Sprintf("license limit reached (%d/%d in use) — raise the license or decommission a device", len(s.byID), max)
		}
	}

	// Console computer id: keep the existing one on dedup; assign the next if the
	// device is new OR the matched record has none yet (e.g. a bare record created
	// by an earlier connect/heartbeat before enrollment).
	computerID := ""
	if existing != nil {
		computerID = existing.ComputerID
	}
	if strings.TrimSpace(computerID) == "" {
		computerID = s.newComputerID() // caller holds s.mu
	}

	rec := &deviceRecord{
		DeviceID:    assigned,
		SystemUUID:  req.SystemUuid,
		MachineGUID: req.MachineGuid,
		TpmEK:       req.TpmEk,
		ComputerID:  computerID,
		Hostname:    req.Hostname,
		LastSeen:    time.Now(),
	}
	s.byID[assigned] = rec
	if req.SystemUuid != "" {
		s.bySysUUID[req.SystemUuid] = rec
	}
	if req.TpmEk != "" {
		s.byTPM[req.TpmEk] = rec
	}
	s.saveLocked() // durable, so the dedup survives a restart (DB stand-in)
	return assigned, deduped, ""
}

// computerIDFor returns the console numeric id assigned to a device (for the
// Enroll response). Caller holds no lock.
func (s *server) computerIDFor(deviceID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.byID[deviceID]; r != nil {
		return r.ComputerID
	}
	return ""
}

// identify returns a compact "device / computer_id / host" label for a device_id,
// resolved from the registry. Every agent message carries the device_id; the SERVER
// owns the mapping to the console computer_id and the current hostname (like a real
// UEM console), so it prints all three for any command/action without the agent
// having to repeat them on the wire. Unknown fields show "?" until enroll/heartbeat
// populates them.
func (s *server) identify(deviceID string) string {
	cid, host := "?", "?"
	s.mu.Lock()
	if r := s.byID[deviceID]; r != nil {
		if r.ComputerID != "" {
			cid = r.ComputerID
		}
		if r.Hostname != "" {
			host = r.Hostname
		}
	}
	s.mu.Unlock()
	return fmt.Sprintf("device=%s computer_id=%s host=%s", deviceID, cid, host)
}

// newComputerID returns a fresh, unique, RANDOM 10-digit console id. Random (not
// sequential) so a customer cannot infer the enrolled-device count and no device
// looks like "the first computer". Caller holds s.mu. The real server does the
// same against its DB (unique index + retry on collision).
func (s *server) newComputerID() string {
	for {
		id := randomComputerID()
		if _, used := s.computerIDUsed[id]; !used {
			s.computerIDUsed[id] = struct{}{}
			return id
		}
	}
}

// randomComputerID returns a random 10-digit numeric string in [1000000000,
// 9999999999] — always 10 digits (never a leading zero).
func randomComputerID() string {
	const lo, span = int64(1000000000), int64(9000000000) // [1e9, 1e10)
	n, err := rand.Int(rand.Reader, big.NewInt(span))
	if err != nil {
		// crypto/rand should not fail; fall back to a time-derived value rather than panic.
		return strconv.FormatInt(lo+time.Now().UnixNano()%span, 10)
	}
	return strconv.FormatInt(lo+n.Int64(), 10)
}

func (s *server) deviceCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byID)
}

// certTTL is the issued client-cert validity. Default ~398 days (BigFix
// convention); AEGIS_CERT_TTL_SECONDS overrides (small values make renewal
// observable in seconds for testing).
func certTTL() time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("AEGIS_CERT_TTL_SECONDS"))); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 398 * 24 * time.Hour
}

// signClientCert signs a client cert for the given CN with the CA.
func (s *server) signClientCert(cn string, pub any) (string, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(certTTL()),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, s.caCert, pub, s.caKey)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), nil
}

// clientCertCN returns the device_id (CN) of the verified client cert on the mTLS
// connection, or "" if none.
func clientCertCN(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return ""
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.VerifiedChains) == 0 || len(tlsInfo.State.VerifiedChains[0]) == 0 {
		return ""
	}
	return tlsInfo.State.VerifiedChains[0][0].Subject.CommonName
}

// Renew re-issues the client cert for an already-authenticated device (over mTLS,
// no token). The real server adds revocation/lifecycle checks here.
func (s *server) Renew(ctx context.Context, req *pb.RenewRequest) (*pb.RenewResponse, error) {
	cn := clientCertCN(ctx)
	if cn == "" {
		return &pb.RenewResponse{Error: "renew requires a verified client certificate"}, nil
	}
	block, _ := pem.Decode([]byte(req.CsrPem))
	if block == nil {
		return &pb.RenewResponse{Error: "bad CSR PEM"}, nil
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return &pb.RenewResponse{Error: "invalid CSR"}, nil
	}
	// Sign for the AUTHENTICATED device_id (cert CN), not whatever the CSR claims.
	certPEM, err := s.signClientCert(cn, csr.PublicKey)
	if err != nil {
		return &pb.RenewResponse{Error: "sign cert: " + err.Error()}, nil
	}
	mockPrintf("Renew OK -> device_id=%s (new cert signed, valid %s)\n", cn, certTTL())
	return &pb.RenewResponse{ClientCertPem: certPEM}, nil
}

// licenseMax reads the license cap (AEGIS_LICENSE_MAX); 0 / unset = unlimited.
func licenseMax() int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("AEGIS_LICENSE_MAX")))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// Enroll is the tiny-vault reference: it validates the bootstrap token, signs the
// agent's CSR with the CA, and returns a per-device client cert. The real server
// also enforces licensing + device reconciliation here (see
// docs/device-identity-and-lifecycle.md).
func (s *server) Enroll(_ context.Context, req *pb.EnrollRequest) (*pb.EnrollResponse, error) {
	want := strings.TrimSpace(os.Getenv("AEGIS_ENROLL_TOKEN"))
	if want == "" || req.EnrollToken != want {
		mockPrintf("Enroll REJECTED -> device_id=%s (bad/missing token)\n", req.DeviceId)
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
	// Reconcile to a single logical device (dedup by device_id then system_uuid)
	// and enforce the license cap.
	assigned, deduped, rejected := s.reconcile(req)
	if rejected != "" {
		mockPrintf("Enroll REJECTED -> device_id=%s hostname=%s: %s\n", req.DeviceId, req.Hostname, rejected)
		return &pb.EnrollResponse{Error: rejected}, nil
	}

	// Sign for the ASSIGNED id (not the CSR's CN) — a reconciled device gets a cert
	// bound to its canonical id.
	certPEM, err := s.signClientCert(assigned, csr.PublicKey)
	if err != nil {
		return &pb.EnrollResponse{Error: "sign cert: " + err.Error()}, nil
	}
	lic := ""
	if max := licenseMax(); max > 0 {
		lic = fmt.Sprintf(" [license %d/%d]", s.deviceCount(), max)
	}
	computerID := s.computerIDFor(assigned)
	if deduped {
		mockPrintf("Enroll OK (RECONCILED/dedup) -> requested=%s assigned=%s computer_id=%s tpm_ek=%s system_uuid=%s hostname=%s schema_version=%s device_type=%s capabilities=%d (same hardware → existing device)%s\n",
			req.DeviceId, assigned, computerID, shortID(req.TpmEk), req.SystemUuid, req.Hostname,
			req.SchemaVersion, req.DeviceType, len(req.AgentCapabilities), lic)
	} else {
		mockPrintf("Enroll OK -> device_id=%s computer_id=%s tpm_ek=%s system_uuid=%s machine_guid=%s hostname=%s os=%s schema_version=%s device_type=%s capabilities=%d (cert signed)%s\n",
			assigned, computerID, shortID(req.TpmEk), req.SystemUuid, req.MachineGuid, req.Hostname, req.Os,
			req.SchemaVersion, req.DeviceType, len(req.AgentCapabilities), lic)
	}
	return &pb.EnrollResponse{
		ClientCertPem:    string(certPEM),
		CaChainPem:       string(s.caCertPEM),
		AssignedDeviceId: assigned,
		ComputerId:       computerID,
	}, nil
}

// shortID trims a long hardware id for readable logging. "" stays "".
func shortID(s string) string {
	if len(s) > 20 {
		return s[:20] + "…"
	}
	return s
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
	case "nsh-file":
		s.pendingMu.Lock()
		content, ok := s.fileContent[req.Id]
		s.pendingMu.Unlock()
		if !ok || content.deviceID != clientCertCN(stream.Context()) || content.sha256 != req.Sha256 {
			return status.Error(codes.PermissionDenied, "file transfer is not authorized for this device/content")
		}
		path = content.path
	case "cab":
		path = os.Getenv("AEGIS_SERVE_CAB")
	case "patch":
		path = os.Getenv("AEGIS_SERVE_PATCH")
	case "package":
		// Software distribution: the installer the admin put in the catalog. The
		// agent streams it (no internet), stores it encrypted, verifies the hash.
		planPath, planned, planErr := s.planPackageContent(clientCertCN(stream.Context()), req.Id, req.Sha256)
		if planErr != nil {
			return status.Error(codes.PermissionDenied, planErr.Error())
		}
		if planned {
			path = planPath
		} else {
			path = os.Getenv("AEGIS_SERVE_SOFTWARE")
		}
	}
	if path == "" {
		return fmt.Errorf("no file configured to serve for kind %q (set AEGIS_SERVE_CAB/_PATCH/_SOFTWARE)", req.Kind)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	mockPrintf("FetchContent: serving %s (kind=%s, id=%s)\n", path, req.Kind, req.Id)
	// Checkpoint restart: the agent resumes an interrupted transfer by asking for
	// the bytes it does not yet have. Seek to that offset and stream only the
	// remainder. An offset past the file end is rejected so the agent restarts
	// cleanly from zero rather than silently receiving nothing.
	if req.Offset > 0 {
		info, serr := f.Stat()
		if serr != nil {
			return serr
		}
		if req.Offset > info.Size() {
			return status.Errorf(codes.OutOfRange, "resume offset %d is past the end of the %d-byte file", req.Offset, info.Size())
		}
		if _, serr := f.Seek(req.Offset, io.SeekStart); serr != nil {
			return serr
		}
		mockPrintf("FetchContent: resuming from offset %d (%d bytes remain)\n", req.Offset, info.Size()-req.Offset)
	}
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
	mockPrintf("FetchContent: done (%d bytes)\n", total)
	return nil
}

// UploadContent receives one endpoint file from an agent (the collect_file task) —
// the on-demand reverse of FetchContent. Stored under
// uploads/<device>/<action_id>/<name>; the end-to-end SHA-256 the agent sent on the
// final chunk is verified. The real server persists these in its artifact store.
func (s *server) UploadContent(stream pb.AgentControlPlane_UploadContentServer) error {
	if !clientVerified(stream.Context()) {
		return status.Error(codes.Unauthenticated, "client certificate required")
	}
	device := clientCertCN(stream.Context())

	// Folder is the console computer_id (unique, stable) — hostnames can collide
	// across a fleet, so the server team keys collected files by computer_id and
	// resolves which machine it is from the console inventory.
	folder := sanitizePathSeg(device)
	s.mu.Lock()
	if r := s.byID[device]; r != nil && r.ComputerID != "" {
		folder = sanitizePathSeg(r.ComputerID)
	}
	s.mu.Unlock()

	var (
		f        *os.File
		path     string
		h        = sha256.New()
		total    int64
		wantSha  string
		actionID string
	)
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			if f != nil {
				f.Close()
				os.Remove(path)
			}
			return err
		}
		if f == nil {
			// NSH destinations are chosen by the operator and bound to this device.
			// Other collection tasks retain the action-ID storage layout.
			actionID = sanitizePathSeg(chunk.ActionId)
			name := sanitizePathSeg(chunk.Name)
			dir := filepath.Join("uploads", folder, actionID)
			s.pendingMu.Lock()
			target, nsh := s.nshUploads[chunk.ActionId]
			s.pendingMu.Unlock()
			if nsh && target.deviceID == device {
				rel, err := safeNSHUploadPath(target.path)
				if err != nil {
					return stream.SendAndClose(&pb.UploadAck{Error: err.Error()})
				}
				dir = filepath.Join("uploads", folder, filepath.Dir(rel))
				name = filepath.Base(rel)
			}
			if name == "" {
				return stream.SendAndClose(&pb.UploadAck{Error: "missing file name on first chunk"})
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return stream.SendAndClose(&pb.UploadAck{Error: "store dir: " + err.Error()})
			}
			if f, path, err = createUploadFile(dir, name); err != nil {
				return stream.SendAndClose(&pb.UploadAck{Error: "create: " + err.Error()})
			}
			defer f.Close()
		}
		if len(chunk.Data) > 0 {
			if _, err := f.Write(chunk.Data); err != nil {
				return stream.SendAndClose(&pb.UploadAck{Error: "write: " + err.Error()})
			}
			h.Write(chunk.Data)
			total += int64(len(chunk.Data))
		}
		if chunk.Sha256Full != "" {
			wantSha = chunk.Sha256Full
		}
	}
	if f == nil {
		return stream.SendAndClose(&pb.UploadAck{Error: "no chunks received"})
	}
	got := hex.EncodeToString(h.Sum(nil))
	ok := wantSha != "" && strings.EqualFold(got, wantSha)
	if ok {
		s.pendingMu.Lock()
		target, exists := s.nshUploads[actionID]
		if exists && target.deviceID == device && strings.HasPrefix(target.path, "_nsh-transfer/") {
			if s.fileReceipts == nil {
				s.fileReceipts = make(map[string]fileReceipt)
			}
			s.fileReceipts[actionID] = fileReceipt{path: path, sha256: got, deviceID: device}
		}
		s.pendingMu.Unlock()
	}
	mockPrintf("Collected file -> %s | action_id: %s, stored: %s, bytes: %d, integrity: %v\n",
		s.identify(device), actionID, path, total, ok)
	ack := &pb.UploadAck{StoredPath: path, ReceivedBytes: total, IntegrityOk: ok}
	if !ok {
		ack.Error = "sha256 mismatch (or missing) on upload"
	}
	return stream.SendAndClose(ack)
}

// fileSHA256 returns the hex sha256 of a local file — the mock's stand-in for the
// console computing the package hash at upload time (the admin never types it).
func fileSHA256(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("no file to hash")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// randomShellSessionID returns a fresh 32-byte hex session_id for an open_shell
// command. One-time: never reused across sessions (replay would be refused).
func randomShellSessionID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// int64FromEnvDefault parses an integer env var; empty/invalid → the fallback.
func int64FromEnvDefault(name string, fallback int64) int64 {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

// ShellSession is the live remote-shell pipe (BladeLogic nexec model, not RDP).
// The agent dials in with the session_id we gave it on open_shell; the mock
// console (local stdin) becomes the admin, and shell output prints live. This
// is the EXECUTABLE CONTRACT for the real server + console UI.
func (s *server) ShellSession(stream pb.AgentControlPlane_ShellSessionServer) error {
	if !clientVerified(stream.Context()) {
		return status.Error(codes.Unauthenticated, "client certificate required")
	}
	device := clientCertCN(stream.Context())

	// First chunk MUST carry the session_id. Match it against a parked open_shell
	// (single-use; a replayed session_id is refused).
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	sid := first.SessionId
	if sid == "" {
		return status.Error(codes.InvalidArgument, "first ShellOutput must carry session_id")
	}
	s.shellMu.Lock()
	parked, ok := s.shellPending[sid]
	if ok {
		delete(s.shellPending, sid) // single-use — consume on dial
	}
	s.shellMu.Unlock()
	if !ok {
		mockPrintf("ShellSession REFUSED (unknown or already-consumed session_id) -> %s\n", s.identify(device))
		return status.Error(codes.PermissionDenied, "unknown or already-consumed session_id")
	}
	mockPrintf("ShellSession OPEN -> %s (session=%s, since open_shell=%s)\n",
		s.identify(device), sid[:12]+"...", time.Since(parked.started).Round(time.Millisecond))
	mockPrintln("---- type commands below (end with Ctrl+Z then Enter to close) ----")

	// agent → mock (stdout/stderr/close): print live, mark the session closed on
	// the terminal chunk so our stdin reader bails out.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			out, err := stream.Recv()
			if err != nil {
				if err != io.EOF {
					mockPrintf("ShellSession recv error: %v\n", err)
				}
				return
			}
			if len(out.Stdout) > 0 {
				os.Stdout.Write(out.Stdout)
			}
			if len(out.Stderr) > 0 {
				os.Stderr.Write(out.Stderr)
			}
			if out.Closed {
				mockPrintf("\n---- shell closed (exit=%d, reason=%s) ----\n", out.ExitCode, out.Reason)
				return
			}
		}
	}()

	// mock stdin → agent (stdin). One blocking read loop; terminates when the
	// agent closes the stream (done signaled) or stdin hits EOF.
	in := make([]byte, 4096)
	for {
		select {
		case <-done:
			return nil
		default:
		}
		n, rerr := os.Stdin.Read(in)
		if n > 0 {
			if serr := stream.Send(&pb.ShellInput{Stdin: append([]byte{}, in[:n]...)}); serr != nil {
				mockPrintf("ShellSession send error: %v\n", serr)
				return serr
			}
		}
		if rerr != nil {
			// Ctrl+Z (Windows EOF on stdin) → send clean close signal.
			_ = stream.Send(&pb.ShellInput{Control: "close"})
			<-done
			return nil
		}
	}
}

// sanitizePathSeg makes one path segment safe for local storage: path separators
// and parent references are stripped so an uploaded name cannot escape uploads/.
func sanitizePathSeg(s string) string {
	s = strings.ReplaceAll(s, "\\", "/")
	s = filepath.Base(s) // keeps only the last element; "." for empty/dot paths
	if s == "." || s == ".." {
		return ""
	}
	return s
}

func (s *server) Connect(stream pb.AgentControlPlane_ConnectServer) error {
	if !clientVerified(stream.Context()) {
		mockPrintln("Connect REJECTED -> no verified client certificate (must enroll first)")
		return status.Error(codes.Unauthenticated, "client certificate required")
	}
	connectedDevice := clientCertCN(stream.Context())
	mockPrintf("Agent connected! device: %s\n", connectedDevice)

	// Register this connection in the live-agent registry so the NSH REPL can
	// target it by hostname / device_id / computer_id. Deregister on return.
	connection := s.registerLiveAgent(connectedDevice, stream)
	defer s.deregisterLiveAgent(connectedDevice)

	go func() {
		// Device targeting for testing: if AEGIS_TARGET_DEVICE is set, only send
		// commands to that device_id (others just stay connected / receive). The
		// REAL server targets by device; the mock otherwise sends to whoever connects.
		if target := strings.TrimSpace(os.Getenv("AEGIS_TARGET_DEVICE")); target != "" && !strings.EqualFold(target, connectedDevice) {
			mockPrintf("  (AEGIS_TARGET_DEVICE=%s set; connected device=%s — receiving only, NO commands sent)\n", target, connectedDevice)
			return
		}
		time.Sleep(2 * time.Second)
		payload := []byte("")
		command := &pb.ServerMessage{
			Payload: &pb.ServerMessage_Command{
				Command: sign(&pb.Command{
					CommandId: "cmd-001",
					Type:      "hostname",
					Payload:   payload,
				}),
			},
		}
		if err := connection.send(command); err != nil {
			log.Printf("failed to send command: %v", err)
			return
		}
		mockPrintln("Sent command -> id: cmd-001, type: hostname (signed)")

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
						Command: sign(&pb.Command{
							CommandId: cmdID,
							Type:      "install_patch",
							Payload:   specBytes,
						}),
					},
				}
				if err := connection.send(installCmd); err != nil {
					log.Printf("failed to send install command: %v", err)
					return
				}
				mockPrintf("Sent command -> id: %s, type: install_patch, kb=%s, file=%s (signed)\n", cmdID, spec.Kb, f)
			}
		}

		// Optionally push one signed set_policy command (Signed Agent Policy A1).
		// AEGIS_SET_POLICY=1 enables it; AEGIS_POLICY_ID, AEGIS_POLICY_ISSUED_AT
		// (default now), AEGIS_POLICY_REQUIRE_FIPS (1), AEGIS_POLICY_DENY /
		// AEGIS_POLICY_ALLOW (comma-separated command types), AEGIS_POLICY_REBOOT
		// (never|if_required|always) shape it.
		if strings.TrimSpace(os.Getenv("AEGIS_SET_POLICY")) != "" {
			// env trims trailing whitespace so `set A=1 & set B=2` chaining in cmd.exe
			// (which captures the space before `&` into the value) still parses — a
			// trailing space otherwise broke every numeric field (e.g. issued_at fell
			// back to time.Now() and the policy was rejected as a rollback).
			env := func(k string) string { return strings.TrimSpace(os.Getenv(k)) }
			pol := policy.Policy{Version: 1}
			pol.PolicyID = env("AEGIS_POLICY_ID")
			if pol.PolicyID == "" {
				pol.PolicyID = fmt.Sprintf("pol-%d", time.Now().Unix())
			}
			pol.IssuedAt = time.Now().Unix()
			if v, err := strconv.ParseInt(env("AEGIS_POLICY_ISSUED_AT"), 10, 64); err == nil && v > 0 {
				pol.IssuedAt = v
			}
			pol.SecurityFloor.RequireFIPS = env("AEGIS_POLICY_REQUIRE_FIPS") != ""
			if d := env("AEGIS_POLICY_DENY"); d != "" {
				pol.Modules.Deny = strings.Split(d, ",")
			}
			if a := env("AEGIS_POLICY_ALLOW"); a != "" {
				pol.Modules.Allow = strings.Split(a, ",")
			}
			pol.Patch.RebootPolicy = env("AEGIS_POLICY_REBOOT")
			if v, err := strconv.ParseInt(env("AEGIS_POLICY_FETCH_MAX_BPS"), 10, 64); err == nil && v > 0 {
				pol.Transfer.FetchMaxBPS = v
			}
			if v, err := strconv.ParseInt(env("AEGIS_POLICY_UPLOAD_MAX_BPS"), 10, 64); err == nil && v > 0 {
				pol.Transfer.UploadMaxBPS = v
			}
			if lvl := env("AEGIS_POLICY_LOG_LEVEL"); lvl != "" {
				pol.Logging.Level = lvl
			}
			// Declared (◻️) fields — accepted + reported today, enforcement wiring
			// incremental. Present here so a tester can round-trip them end to end.
			if v := env("AEGIS_POLICY_MIN_TLS"); v != "" {
				pol.SecurityFloor.MinTLS = v
			}
			if d := env("AEGIS_POLICY_MAINT_DAYS"); d != "" {
				pol.Maintenance.Days = strings.Split(d, ",")
			}
			if n, err := strconv.Atoi(env("AEGIS_POLICY_MAINT_START")); err == nil {
				pol.Maintenance.StartHour = n
			}
			if n, err := strconv.Atoi(env("AEGIS_POLICY_MAINT_END")); err == nil {
				pol.Maintenance.EndHour = n
			}
			if n, err := strconv.Atoi(env("AEGIS_POLICY_AUDIT_RATE")); err == nil && n > 0 {
				pol.Audit.RatePerSec = n
			}
			if n, err := strconv.Atoi(env("AEGIS_POLICY_AUDIT_BURST")); err == nil && n > 0 {
				pol.Audit.Burst = n
			}
			if v, err := strconv.ParseInt(env("AEGIS_POLICY_AUDIT_DEDUP"), 10, 64); err == nil && v > 0 {
				pol.Audit.DedupWindowSeconds = v
			}
			if v, err := strconv.ParseInt(env("AEGIS_POLICY_INV_FULL_INTERVAL"), 10, 64); err == nil && v > 0 {
				pol.Inventory.FullIntervalSeconds = v
			}
			payload, err := json.Marshal(pol)
			if err != nil {
				log.Printf("failed to marshal policy: %v", err)
			} else {
				polCmd := &pb.ServerMessage{Payload: &pb.ServerMessage_Command{Command: sign(&pb.Command{
					CommandId: "policy-001", Type: "set_policy", Payload: payload,
				})}}
				if err := connection.send(polCmd); err != nil {
					log.Printf("failed to send set_policy command: %v", err)
					return
				}
				mockPrintf("Sent command -> id: policy-001, type: set_policy, policy_id=%s issued_at=%d require_fips=%v deny=%v (signed)\n", pol.PolicyID, pol.IssuedAt, pol.SecurityFloor.RequireFIPS, pol.Modules.Deny)
			}
		}

		// Optionally push one signed remote_control command (RC-0 session plumbing).
		// AEGIS_RC=1 enables it; shape with AEGIS_RC_ACTION (start|stop|status|join|
		// pass_control, default start), AEGIS_RC_SESSION (default rc-sess-001),
		// AEGIS_RC_OPERATOR (default admin-x), AEGIS_RC_MODE (unattended|attended),
		// AEGIS_RC_TOKEN (default = a fresh 15-min v1 token), AEGIS_RC_ELEVATE (1),
		// AEGIS_RC_TARGET (operator for pass_control).
		if os.Getenv("AEGIS_RC") != "" {
			pick := func(k, def string) string {
				if v := strings.TrimSpace(os.Getenv(k)); v != "" {
					return v
				}
				return def
			}
			token := pick("AEGIS_RC_TOKEN", fmt.Sprintf("v1.%d.mocknonce", time.Now().Add(15*time.Minute).Unix()))
			rc := map[string]interface{}{
				"version":     1,
				"action":      pick("AEGIS_RC_ACTION", "start"),
				"session_id":  pick("AEGIS_RC_SESSION", "rc-sess-001"),
				"operator":    pick("AEGIS_RC_OPERATOR", "admin-x"),
				"mode":        pick("AEGIS_RC_MODE", "unattended"),
				"maint_token": token,
				"elevate":     os.Getenv("AEGIS_RC_ELEVATE") != "",
				"target":      strings.TrimSpace(os.Getenv("AEGIS_RC_TARGET")),
			}
			rcPayload, err := json.Marshal(rc)
			if err != nil {
				log.Printf("failed to marshal remote_control: %v", err)
			} else {
				rcCmd := &pb.ServerMessage{Payload: &pb.ServerMessage_Command{Command: sign(&pb.Command{
					CommandId: "rc-001", Type: "remote_control", Payload: rcPayload,
				})}}
				if err := connection.send(rcCmd); err != nil {
					log.Printf("failed to send remote_control command: %v", err)
					return
				}
				mockPrintf("Sent command -> id: rc-001, type: remote_control, action=%s session=%s operator=%s (signed)\n", rc["action"], rc["session_id"], rc["operator"])
			}
		}

		// Optionally push one signed audit_query command (L3 remote audit query).
		// AEGIS_AUDIT_QUERY=1 enables it; shape it with AEGIS_AUDIT_MODE
		// (tail|search|since, default tail), AEGIS_AUDIT_PATTERN, AEGIS_AUDIT_LIMIT
		// (default 20), AEGIS_AUDIT_SINCE_MS, AEGIS_AUDIT_UNTIL_MS, AEGIS_AUDIT_ROTATED
		// (1 = include rotated files). The agent reads ONLY its own audit dir and
		// returns matching metadata (no secrets) in ActionStatus.Detail (JSON array).
		if os.Getenv("AEGIS_AUDIT_QUERY") != "" {
			p := auditQueryPayload{Version: 1}
			p.Mode = os.Getenv("AEGIS_AUDIT_MODE")
			if strings.TrimSpace(p.Mode) == "" {
				p.Mode = "tail"
			}
			p.Pattern = os.Getenv("AEGIS_AUDIT_PATTERN")
			p.Limit = 20
			if n, err := strconv.Atoi(os.Getenv("AEGIS_AUDIT_LIMIT")); err == nil && n > 0 {
				p.Limit = n
			}
			if ms, err := strconv.ParseInt(os.Getenv("AEGIS_AUDIT_SINCE_MS"), 10, 64); err == nil {
				p.SinceMS = ms
			}
			if ms, err := strconv.ParseInt(os.Getenv("AEGIS_AUDIT_UNTIL_MS"), 10, 64); err == nil {
				p.UntilMS = ms
			}
			p.IncludeRotated = os.Getenv("AEGIS_AUDIT_ROTATED") != ""
			payload, err := json.Marshal(p)
			if err != nil {
				log.Printf("failed to marshal audit_query: %v", err)
			} else {
				auditCmd := &pb.ServerMessage{Payload: &pb.ServerMessage_Command{Command: sign(&pb.Command{
					CommandId: "audit-001", Type: "audit_query", Payload: payload,
				})}}
				if err := connection.send(auditCmd); err != nil {
					log.Printf("failed to send audit_query command: %v", err)
					return
				}
				mockPrintf("Sent command -> id: audit-001, type: audit_query, mode=%s pattern=%q limit=%d rotated=%v (signed)\n", p.Mode, p.Pattern, p.Limit, p.IncludeRotated)
			}
		}

		// Optionally push one or more signed uninstall_patch commands. Set
		// AEGIS_UNINSTALL_KB (comma-separated for multiple). The agent removes each
		// installed KB via DISM/CBS (WUA fallback) on the serial worker and reports
		// ActionStatus. AEGIS_UNINSTALL_BUILD (e.g. "22621.7584") identifies a
		// cumulative update's CBS package (RollupFix carries the build, not the KB) —
		// the real server supplies it from the catalog.
		if kbEnv := os.Getenv("AEGIS_UNINSTALL_KB"); kbEnv != "" {
			osBuild := strings.TrimSpace(os.Getenv("AEGIS_UNINSTALL_BUILD"))
			for i, kb := range strings.Split(kbEnv, ",") {
				kb = strings.TrimSpace(kb)
				if kb == "" {
					continue
				}
				specBytes, err := proto.Marshal(&pb.InstallPatchSpec{Kb: kb, OsBuild: osBuild})
				if err != nil {
					log.Printf("failed to marshal uninstall spec: %v", err)
					continue
				}
				cmdID := fmt.Sprintf("uninstall-%03d", i+1)
				cmd := &pb.ServerMessage{Payload: &pb.ServerMessage_Command{Command: sign(&pb.Command{
					CommandId: cmdID, Type: "uninstall_patch", Payload: specBytes,
				})}}
				if err := connection.send(cmd); err != nil {
					log.Printf("failed to send uninstall command: %v", err)
					return
				}
				mockPrintf("Sent command -> id: %s, type: uninstall_patch, kb=%s (signed)\n", cmdID, kb)
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
				stageCmd := &pb.ServerMessage{Payload: &pb.ServerMessage_Command{Command: sign(&pb.Command{
					CommandId: cmdID,
					Type:      "stage_patch",
					Payload:   specBytes,
				})}}
				if err := connection.send(stageCmd); err != nil {
					log.Printf("failed to send stage command: %v", err)
					return
				}
				mockPrintf("Sent command -> id: %s, type: stage_patch, kb=%s (signed)\n", cmdID, kb)
			}
		}

		// Optionally push a signed software_deploy command (software distribution).
		// REAL-UEM path: set AEGIS_DEPLOY_CONTENT_ID + AEGIS_SERVE_SOFTWARE (the
		// installer on the SERVER) — the agent streams the bytes (no local file).
		// LOCAL/dev path: set AEGIS_DEPLOY_FILE to a path on the AGENT machine (pre-
		// staged). Optional: _NAME, _VERSION, _TYPE (msi|exe|ps1|bat|auto), _ACTION
		// (install|uninstall), _ARGS (silent flags), _HASH, _HASH_ALGO,
		// _REQUIRE_SIG (1 = verify Authenticode publisher before running).
		// FILELESS UNINSTALL (no file/content_id): MSI by _PRODUCT_CODE ({GUID}), or a
		// non-MSI app by _UNINSTALL_KEY (registry subkey) or _DISPLAY_NAME (substring).
		deployFile := strings.TrimSpace(os.Getenv("AEGIS_DEPLOY_FILE"))
		deployContentID := strings.TrimSpace(os.Getenv("AEGIS_DEPLOY_CONTENT_ID"))
		deployAction := strings.ToLower(strings.TrimSpace(os.Getenv("AEGIS_DEPLOY_ACTION")))
		deployProductCode := strings.TrimSpace(os.Getenv("AEGIS_DEPLOY_PRODUCT_CODE"))
		deployUninstallKey := strings.TrimSpace(os.Getenv("AEGIS_DEPLOY_UNINSTALL_KEY"))
		deployDisplayName := strings.TrimSpace(os.Getenv("AEGIS_DEPLOY_DISPLAY_NAME"))
		// Fileless uninstall needs neither a file nor a content_id: by MSI ProductCode
		// (msiexec /x {GUID}) or by a non-MSI registry entry (uninstall_key /
		// display_name → the vendor's (Quiet)UninstallString). Trigger for those too.
		uninstallFileless := deployAction == "uninstall" && (deployProductCode != "" || deployUninstallKey != "" || deployDisplayName != "")
		if deployFile != "" || deployContentID != "" || uninstallFileless {
			options, err := readSoftdistTestOptions(os.Getenv)
			if err != nil {
				mockPrintf("Software deploy test settings refused: %v\n", err)
				return
			}
			spec := &pb.SoftwareDeploySpec{
				PackageId:        os.Getenv("AEGIS_DEPLOY_PACKAGE_ID"),
				Name:             os.Getenv("AEGIS_DEPLOY_NAME"),
				Version:          os.Getenv("AEGIS_DEPLOY_VERSION"),
				Action:           os.Getenv("AEGIS_DEPLOY_ACTION"),
				InstallerType:    os.Getenv("AEGIS_DEPLOY_TYPE"),
				FilePath:         deployFile,      // local/pre-staged (dev); empty for server delivery
				ContentId:        deployContentID, // server-delivered (real UEM); streamed via FetchContent
				SilentArgs:       os.Getenv("AEGIS_DEPLOY_ARGS"),
				ExpectedHash:     os.Getenv("AEGIS_DEPLOY_HASH"),
				HashAlgorithm:    os.Getenv("AEGIS_DEPLOY_HASH_ALGO"),
				RequireSignature: truthy(os.Getenv("AEGIS_DEPLOY_REQUIRE_SIG")),
				ProductCode:      os.Getenv("AEGIS_DEPLOY_PRODUCT_CODE"),        // MSI GUID → idempotency check
				UninstallKey:     deployUninstallKey,                            // non-MSI registry subkey
				DisplayName:      deployDisplayName,                             // non-MSI DisplayName match
				Parameters:       parseParams(os.Getenv("AEGIS_DEPLOY_PARAMS")), // ${name} substitution, e.g. "REGION=apac,INSTALLDIR=C:\app"
				TimeoutSeconds:   options.timeout,
				SuccessExitCodes: options.successCodes,
				// Per-task fetch rate cap in bytes/sec (BigFix "lower wins" with
				// agent config). 0 = no per-task cap. Useful for huge installers
				// during business hours: AEGIS_DEPLOY_FETCH_MAX_BYTES_PER_SEC=500000.
				MaxFetchBytesPerSecond: int64FromEnv("AEGIS_DEPLOY_FETCH_MAX_BYTES_PER_SEC"),
				// Per-task script content audit opt-in. When true, the agent
				// writes FULL script bytes to <SKVC>/logs/softdist/<id>.transcript
				// (default is hash+preview only). Compliance / forensic deploys.
				// Endpoint policy AEGIS_LOG_SCRIPT_CONTENT can override:
				// "disabled" force-off, "always" force-on.
				CaptureScriptContent: strings.EqualFold(os.Getenv("AEGIS_DEPLOY_CAPTURE_SCRIPT_CONTENT"), "true"),
			}
			// AUTO-HASH (BigFix model — the console computes the SHA at package upload,
			// the admin never types it): when no AEGIS_DEPLOY_HASH is given for a
			// file-based deploy, compute sha256 from the package the server is about to
			// serve (AEGIS_SERVE_SOFTWARE), or from AEGIS_DEPLOY_FILE when that path is
			// readable on THIS machine (same-box dev). The agent REFUSES unpinned
			// file-based payloads, so without a hash the deploy will fail by design.
			commandType, privateErr := applyPrivateDeployment(spec, os.Getenv)
			if privateErr != nil {
				mockPrintf("Software deploy private settings refused: %v\n", privateErr)
				return
			}
			defer func() { clear(spec.GetExecution().GetPasswordUtf8()); clear(spec.SecureParameters) }()
			bundled, bundleErr := applyMockBundle(spec, os.Getenv)
			if bundleErr != nil {
				mockPrintf("Software bundle settings refused: %v\n", bundleErr)
				return
			}
			if bundled {
				commandType = "software_deploy_bundle"
			}

			workflow, workflowErr := applyMockWorkflow(spec, os.Getenv)
			if workflowErr != nil {
				mockPrintf("Software workflow settings refused: %v\n", workflowErr)
				return
			}
			if workflow {
				commandType = "software_deploy_workflow"
			}

			detected, detectionErr := applyMockDetection(spec, os.Getenv)
			if detectionErr != nil {
				mockPrintf("Software detection settings refused: %v\n", detectionErr)
				return
			}
			if detected {
				commandType = "software_deploy_detect"
			}

			isCommandType := strings.EqualFold(strings.TrimSpace(spec.InstallerType), "command")
			if options.autoHash && spec.ExpectedHash == "" && (!isCommandType || spec.Bundle != nil) && (deployFile != "" || deployContentID != "") {
				src := strings.TrimSpace(os.Getenv("AEGIS_SERVE_SOFTWARE"))
				if src == "" {
					src = deployFile
				}
				if sum, err := fileSHA256(src); err == nil {
					spec.ExpectedHash, spec.HashAlgorithm = sum, "sha256"
					mockPrintf("Auto-computed package sha256 (%s): %s\n", src, sum)
				} else {
					log.Printf("WARNING: no AEGIS_DEPLOY_HASH and cannot read %q to auto-hash (%v) — the agent will REFUSE this unpinned deploy; set AEGIS_DEPLOY_HASH (file is on the agent machine)", src, err)
				}
			}
			if !s.beginDependencyPlan(stream.Context(), connection, spec) {
				specBytes, err := proto.Marshal(spec)
				if commandType != "software_deploy" {
					defer clear(specBytes)
				}
				if err != nil {
					log.Printf("failed to marshal software_deploy spec: %v", err)
				} else {
					deploy := &pb.ServerMessage{Payload: &pb.ServerMessage_Command{Command: sign(&pb.Command{
						CommandId: "deploy-001", Type: commandType, Payload: specBytes,
					})}}
					if err := connection.send(deploy); err != nil {
						log.Printf("failed to send software_deploy command: %v", err)
						return
					}
					if commandType != "software_deploy" {
						clear(specBytes)
						clear(spec.GetExecution().GetPasswordUtf8())
						clear(spec.SecureParameters)
					}
					src := "file=" + deployFile
					if deployContentID != "" {
						src = "content_id=" + deployContentID + " (server-delivered)"
					}
					rate := ""
					if spec.MaxFetchBytesPerSecond > 0 {
						rate = fmt.Sprintf(", fetch_rate=%d B/s", spec.MaxFetchBytesPerSecond)
					}
					audit := ""
					if spec.CaptureScriptContent {
						audit = ", capture_script_content=true"
					}
					mockPrintf("Sent command -> id: deploy-001, type: %s, name=%s, %s%s%s (signed)\n", commandType, spec.Name, src, rate, audit)
				}
			}
		}

		// Optionally push a signed collect_file command (on-demand log/file pickup —
		// the agent uploads the named endpoint files via UploadContent). Set
		// AEGIS_COLLECT_PATHS (';'-separated paths/globs on the AGENT machine, e.g.
		// "C:\temp\*.log;C:\Program Files\App\app.log"). Optional:
		// AEGIS_COLLECT_TAIL (last N bytes of each file), AEGIS_COLLECT_MAX_EACH,
		// AEGIS_COLLECT_MAX_TOTAL (byte caps). Files land under uploads/<computer_id>/<id>/.
		if cp := strings.TrimSpace(os.Getenv("AEGIS_COLLECT_PATHS")); cp != "" {
			var paths []string
			for _, p := range strings.Split(cp, ";") {
				if p = strings.TrimSpace(p); p != "" {
					paths = append(paths, p)
				}
			}
			spec := &pb.CollectFileSpec{
				Paths:         paths,
				TailBytes:     int64FromEnv("AEGIS_COLLECT_TAIL"),
				MaxBytesEach:  int64FromEnv("AEGIS_COLLECT_MAX_EACH"),
				MaxTotalBytes: int64FromEnv("AEGIS_COLLECT_MAX_TOTAL"),
				// Per-task upload rate cap in bytes/sec (BigFix "lower wins"
				// with agent config). 0 = no per-task cap. Useful for large
				// log hauls: AEGIS_COLLECT_UPLOAD_MAX_BYTES_PER_SEC=500000.
				MaxUploadBytesPerSecond: int64FromEnv("AEGIS_COLLECT_UPLOAD_MAX_BYTES_PER_SEC"),
			}
			if specBytes, err := proto.Marshal(spec); err != nil {
				log.Printf("failed to marshal collect_file spec: %v", err)
			} else {
				collect := &pb.ServerMessage{Payload: &pb.ServerMessage_Command{Command: sign(&pb.Command{
					CommandId: "collect-001", Type: "collect_file", Payload: specBytes,
				})}}
				if err := connection.send(collect); err != nil {
					log.Printf("failed to send collect_file command: %v", err)
					return
				}
				rate := ""
				if spec.MaxUploadBytesPerSecond > 0 {
					rate = fmt.Sprintf(", upload_rate=%d B/s", spec.MaxUploadBytesPerSecond)
				}
				mockPrintf("Sent command -> id: collect-001, type: collect_file, paths=%v%s (signed)\n", paths, rate)
			}
		}

		// NOTE: shell / run_command / collect_file are no longer triggered from
		// this per-connect block. They are driven on-demand from the NSH REPL
		// (see runNSH / mockserver.exe nsh) — the real console pattern. Env
		// triggers for soft deploy, collect_file, baseline etc. stay (regression
		// tests the server team already relies on).

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
			if err := connection.send(bl); err != nil {
				log.Printf("failed to send baseline: %v", err)
				return
			}
			mockPrintf("Sent patch baseline -> build=%s, latest_kbs=%v\n", build, kbs)
		}

		// Optionally push the current cab version. Set AEGIS_CAB_VERSION (and
		// optionally AEGIS_CAB_SHA256) to simulate the server advertising a cab.
		if ver := os.Getenv("AEGIS_CAB_VERSION"); ver != "" {
			cm := &pb.ServerMessage{Payload: &pb.ServerMessage_CabManifest{
				CabManifest: &pb.CabManifest{Version: ver, Sha256: os.Getenv("AEGIS_CAB_SHA256")},
			}}
			if err := connection.send(cm); err != nil {
				log.Printf("failed to send cab manifest: %v", err)
				return
			}
			mockPrintf("Sent cab manifest -> version=%s\n", ver)
		}

		// Optionally send a signed reboot command. Set AEGIS_REBOOT_DELAY (seconds)
		// to schedule a reboot (use a large value + then test cancel_reboot, or
		// revert the VM snapshot).
		if rd := os.Getenv("AEGIS_REBOOT_DELAY"); rd != "" {
			delay, _ := strconv.Atoi(rd)
			spec := &pb.RebootSpec{DelaySeconds: int32(delay), Message: "Aegis One: scheduled reboot for patching"}
			b, _ := proto.Marshal(spec)
			reboot := &pb.ServerMessage{Payload: &pb.ServerMessage_Command{Command: sign(&pb.Command{
				CommandId: "reboot-001", Type: "reboot", Payload: b,
			})}}
			if err := connection.send(reboot); err != nil {
				log.Printf("failed to send reboot command: %v", err)
				return
			}
			mockPrintf("Sent command -> id: reboot-001, type: reboot, delay=%ds (signed)\n", delay)
		}

		// Optionally send a signed scan_now command (set AEGIS_SCAN_NOW=1) — forces an
		// on-demand compliance scan on the endpoint (what a console "Scan now" does).
		if os.Getenv("AEGIS_SCAN_NOW") != "" {
			payload := []byte("")
			scan := &pb.ServerMessage{Payload: &pb.ServerMessage_Command{Command: sign(&pb.Command{
				CommandId: "scan-now-001", Type: "scan_now", Payload: payload,
			})}}
			if err := connection.send(scan); err != nil {
				log.Printf("failed to send scan_now: %v", err)
				return
			}
			mockPrintln("Sent command -> id: scan-now-001, type: scan_now (signed)")
		}

		// Optionally send a signed cancel_reboot command (set AEGIS_CANCEL_REBOOT=1).
		if os.Getenv("AEGIS_CANCEL_REBOOT") != "" {
			payload := []byte("")
			cancel := &pb.ServerMessage{Payload: &pb.ServerMessage_Command{Command: sign(&pb.Command{
				CommandId: "cancel-reboot-001", Type: "cancel_reboot", Payload: payload,
			})}}
			if err := connection.send(cancel); err != nil {
				log.Printf("failed to send cancel_reboot: %v", err)
				return
			}
			mockPrintln("Sent command -> id: cancel-reboot-001, type: cancel_reboot (signed)")
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
				ping := &pb.ServerMessage{Payload: &pb.ServerMessage_Command{Command: sign(&pb.Command{
					CommandId: fmt.Sprintf("ping-%03d", i),
					Type:      "hostname",
					Payload:   payload,
				})}}
				if err := connection.send(ping); err != nil {
					log.Printf("failed to send parallel ping: %v", err)
					return
				}
				mockPrintf("Sent parallel ping -> id: ping-%03d, type: hostname (while install may be running)\n", i)
			}
		}
	}()

	for {
		msg, err := stream.Recv()
		if err != nil {
			mockPrintln("Agent disconnected:", err)
			return err
		}
		if hb := msg.GetHeartbeat(); hb != nil {
			mockPrintf("Received heartbeat -> device: %s, hostname: %s, version: %s, os: %s, schema_version: %s, device_type: %s, capabilities: %d\n",
				hb.DeviceId, hb.Hostname, hb.AgentVersion, hb.Os, hb.SchemaVersion, hb.DeviceType, len(hb.AgentCapabilities))
			// Attribute update from heartbeat: hostname is mutable and NOT the key.
			// A rename keeps the same device record — only the attribute changes.
			s.mu.Lock()
			rec := s.byID[hb.DeviceId]
			if rec == nil {
				s.byID[hb.DeviceId] = &deviceRecord{DeviceID: hb.DeviceId, Hostname: hb.Hostname, LastSeen: time.Now()}
				mockPrintf("  device %s seen via heartbeat (registered), hostname: %s\n", hb.DeviceId, hb.Hostname)
			} else if hb.Hostname != "" && rec.Hostname != hb.Hostname {
				mockPrintf("  device %s RENAMED: %q -> %q (same record — hostname is just an attribute)\n", hb.DeviceId, rec.Hostname, hb.Hostname)
				rec.Hostname = hb.Hostname
			}
			s.mu.Unlock()
		}
		if result := msg.GetCommandResult(); result != nil {
			// If the REPL is waiting on this command_id (e.g. a stale agent
			// returned "unsupported command type" via CommandResult instead of
			// ActionStatus), forward it as a synthetic terminal FAILED status
			// so the REPL fails FAST (< 1s) instead of waiting 310s for an
			// ActionStatus that will never come. Also fed to the normal print
			// path when NOT NSH-matched.
			synth := &pb.ActionStatus{
				ActionId: result.CommandId, DeviceId: connectedDevice,
				Phase:  pb.ActionPhase_ACTION_PHASE_FAILED,
				Reason: result.Message,
			}
			if result.Status == "success" {
				synth.Phase = pb.ActionPhase_ACTION_PHASE_SUCCESS
			}
			if s.deliverPlanStatus(connectedDevice, synth) {
				continue
			}
			if s.deliverActionStatus(synth) {
				continue
			}
			// CommandResult carries no device_id, but this stream belongs to one agent —
			// resolve all three identifiers from the connected device.
			mockPrintf("Received command result -> %s | command_id: %s, status: %s, message: %s\n",
				s.identify(connectedDevice), result.CommandId, result.Status, result.Message)
		}
		if st := msg.GetActionStatus(); st != nil {
			if s.deliverPlanStatus(connectedDevice, st) {
				continue
			}
			// Forward to any NSH REPL waiting on this action_id — the REPL
			// consumes terminal phases quietly so command output is clean.
			if s.deliverActionStatus(st) {
				continue
			}
			mockPrintf("Received action status -> %s | action_id: %s, type: %s, kb: %s, phase: %s, exit_code: %d, reboot_required: %v, reason: %s\n",
				s.identify(st.DeviceId), st.ActionId, st.ActionType, st.Kb, st.Phase, st.ExitCode, st.RebootRequired, st.Reason)
			if st.PackageDirectory != "" {
				mockPrintf("  package_directory: %s, cleanup: %s\n", st.PackageDirectory, st.PackageCleanup)
			}
			if st.ActionType == "audit_query" {
				// Pretty-print the returned audit events one per line instead of the
				// raw JSON array, so the standalone (non-NSH) audit test is readable.
				printAuditEvents(st.Detail)
			} else if st.Detail != "" {
				mockPrintf("  detail: %s\n", st.Detail)
			}
		}
		if ds := msg.GetDeviceState(); ds != nil {
			mockPrintf("Received patch state report -> %s | product: %s, build: %s, arch: %s, reboot_pending: %v, installed_apps: %d, installed_kbs: %d",
				s.identify(ds.DeviceId), ds.ProductName, ds.Build, ds.Architecture, ds.RebootPending, len(ds.InstalledApps), len(ds.GetInstalledKbs()))
			if ds.RebootPending {
				mockPrintf(" (sources=%v, awaiting_kbs=%v)", ds.RebootPendingSources, ds.RebootPendingKbs)
			}
			mockPrintln()
			// Cache the most recent inventory per device so NSH `inventory apps`
			// can query it ad-hoc. Writes are small (a snapshot pointer swap);
			// reads happen from the REPL goroutine so a mutex is enough.
			s.inventoryMu.Lock()
			if s.lastInventory == nil {
				s.lastInventory = make(map[string]*pb.DeviceStateReport)
			}
			s.lastInventory[ds.DeviceId] = ds
			s.inventoryMu.Unlock()
		}
		if fi := msg.GetFullInventory(); fi != nil {
			hw := fi.GetHardware()
			mockPrintf("Received FULL inventory -> %s | trigger: %s, cpu: %s %s %dMHz (%d/%d cores, %d thr/core, %s), ram: %d MB, disks: %d, nics: %d, apps: %d, OS: %q %q build=%q, virt: %s, system: %s %s\n",
				s.identify(fi.DeviceId), fi.Trigger,
				hw.GetCpuVendor(), hw.GetCpuModel(), hw.GetCpuMhz(),
				hw.GetCpuPhysicalCores(), hw.GetCpuLogicalCores(),
				hw.GetCpuThreadsPerCore(), hw.GetCpuArchitecture(),
				hw.GetMemoryTotalBytes()/(1024*1024),
				len(hw.GetDisks()), len(hw.GetNics()),
				len(fi.InstalledApps),
				hw.GetOsProductName(), hw.GetOsDisplayVersion(), hw.GetOsBuild(),
				hw.GetVirtualization(),
				hw.GetSystemVendor(), hw.GetSystemProduct())
			mockPrintf("  BIOS: %s %s (%s) | drivers: %d | appx: %d\n",
				hw.GetBiosVendor(), hw.GetBiosVersion(), hw.GetBiosReleaseDate(), len(fi.GetDrivers()), len(fi.GetInstalledAppx()))
			if id := fi.GetIdentity(); id != nil {
				mockPrintf("  identity: user=%q domain=%q joined=%v uptime=%ds\n",
					id.GetLoggedOnUser(), id.GetMachineDomain(), id.GetDomainJoined(), id.GetUptimeSeconds())
			}
			if dh := fi.GetDexHealth(); dh != nil {
				mockPrintf("  dex-health: unexpected_shutdowns=%d bugchecks=%d dirty_power=%d (last %dd)\n",
					dh.GetUnexpectedShutdowns(), dh.GetBugchecks(), dh.GetDirtyPowerEvents(), dh.GetWindowDays())
			}
			shownAppx := fi.GetInstalledAppx()
			for i, a := range shownAppx {
				if i >= 12 {
					mockPrintf("  ... and %d more appx package(s)\n", len(shownAppx)-12)
					break
				}
				name := a.GetDisplayName()
				if name == "" {
					name = a.GetName()
				}
				mockPrintf("  appx %-34s v%-16s %-8s [%s]\n",
					truncate(name, 34), a.GetVersion(), a.GetArchitecture(), a.GetOrigin())
			}
			for _, d := range hw.GetDisks() {
				mockPrintf("  disk %s | %s | %s | %.1f GB (%.1f GB free)\n",
					d.MountPoint, d.DriveType, d.FileSystem,
					float64(d.TotalBytes)/(1024*1024*1024),
					float64(d.FreeBytes)/(1024*1024*1024))
			}
			// Driver inventory: print the first few so a tester can eyeball the
			// OEM driver coverage; full list is cached for NSH inspection.
			shownDrivers := fi.GetDrivers()
			for i, d := range shownDrivers {
				if i >= 15 {
					mockPrintf("  ... and %d more driver(s)\n", len(shownDrivers)-15)
					break
				}
				mockPrintf("  driver %-36s v%-16s %-12s %-14s signed=%v [%s]\n",
					truncate(d.GetDeviceName(), 36), d.GetDriverVersion(), d.GetDriverDate(),
					truncate(d.GetProviderName(), 14), d.GetIsSigned(), d.GetDeviceClass())
			}
			s.inventoryMu.Lock()
			if s.lastFullInventory == nil {
				s.lastFullInventory = make(map[string]*pb.FullInventoryReport)
			}
			s.lastFullInventory[fi.DeviceId] = fi
			s.inventoryMu.Unlock()
		}
		if ae := msg.GetAuditEvent(); ae != nil {
			mockPrintf("AUDIT -> %s | ts=%d event=%s cmd_id=%s cmd_type=%s actor=%s source=%s sig_ok=%v decision=%s reason=%q details=%q\n",
				s.identify(ae.DeviceId), ae.Ts, ae.EventType, ae.CommandId, ae.CommandType,
				ae.Actor, ae.Source, ae.SignatureOk, ae.Decision, ae.Reason, ae.Details)
			// server-authoritative audit log: for the demo mock we just
			// print; server-team's server_audit.log is where the real
			// authoritative record lives.
		}
		if cf := msg.GetCacheFull(); cf != nil {
			mockPrintf("Received CACHE-FULL report -> device: %s, pending: %s, needed: %d, cap: %d, in_use: %d, msg: %s\n",
				cf.DeviceId, cf.PendingId, cf.NeededBytes, cf.CapBytes, cf.InUseBytes, cf.Message)
		}
		if psr := msg.GetPatchScanResult(); psr != nil {
			// Count the console-facing categories the agent now tags, so the
			// reference output shows what a real console would categorize.
			var actionable, superseded int
			for _, m := range psr.Missing {
				if m.Superseded {
					superseded++
				} else {
					actionable++
				}
			}
			mockPrintf("Received patch scan result -> device: %s, build: %s, arch: %s, source: %s, missing: %d, superseded: %d, INSTALLED: %d\n",
				psr.DeviceId, psr.Build, psr.Architecture, psr.Source, actionable, superseded, len(psr.Installed))
			for _, m := range psr.Missing {
				tag := "MISSING"
				if m.Superseded {
					tag = "SUPERSEDED"
				}
				mockPrintf("    - [%s][%s] %s (KB=%v, MS=%v, severity=%s, size=%.1f-%.1fMB, reboot=%v, cves=%v, supersedes=%v)\n",
					tag, m.Classification, m.Title, m.KbArticleIds, m.SecurityBulletinIds, m.MsrcSeverity,
					m.MinSizeMb, m.SizeMb, m.RebootRequired, m.CveIds, m.SupersededUpdateIds)
			}
			for _, m := range psr.Installed {
				mockPrintf("    - [INSTALLED][%s] %s (KB=%v, severity=%s)\n",
					m.Classification, m.Title, m.KbArticleIds, m.MsrcSeverity)
			}
		}
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "pack" {
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, "usage: mockserver.exe pack <source-folder> <new-bundle.zip>")
			os.Exit(2)
		}
		sum, err := softdist.PackBundle(context.Background(), os.Args[2], os.Args[3], softdist.BundleLimits{})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Printf("Bundle created: %s\nSHA256: %s\n", os.Args[3], sum)
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "secrets" {
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: mockserver.exe secrets <new-file.dpapi>")
			os.Exit(2)
		}
		if err := createDeploymentSecrets(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}

	if _, err := readSoftdistTestOptions(os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	mode, err := parseMockMode(os.Args[1:], os.Getenv("AEGIS_NSH_SCRIPT"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprint(os.Stderr, mockModeUsage)
		os.Exit(2)
	}
	if mode.help {
		fmt.Print(mockModeUsage)
		return
	}
	serverLog, err := configureMockLogging(mode.nsh)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mockserver: cannot open server log: %v\n", err)
		os.Exit(1)
	}
	if serverLog != nil {
		defer serverLog.Close()
	}
	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		mockFatalf("failed to listen: %v", err)
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
		mockFatalf("TLS setup failed (run certgen first?): %v", err)
	}

	// Allow the agent's keepalive pings (client pings ~60s; without a permissive
	// policy the server would GOAWAY with "too_many_pings"). The real server needs
	// an equivalent policy.
	// The tiny-vault reference: load the CA so Enroll can sign agent CSRs.
	caKeyFile := getenv("AEGIS_CA_KEY", "certs/ca.key")
	caCert, caKey, caCertPEM, err := loadCA(caFile, caKeyFile)
	if err != nil {
		mockFatalf("CA load failed (needed for enrollment): %v", err)
	}
	srv := &server{
		caCert: caCert, caKey: caKey, caCertPEM: caCertPEM,
		statePath:      getenv("AEGIS_MOCK_STATE", "mockserver-devices.json"),
		byID:           make(map[string]*deviceRecord),
		bySysUUID:      make(map[string]*deviceRecord),
		byTPM:          make(map[string]*deviceRecord),
		computerIDUsed: make(map[string]struct{}),
		shellPending:   make(map[string]*shellSession),
		liveAgents:     make(map[string]*liveAgent),
		pending:        make(map[string]chan *pb.ActionStatus),
	}
	// Restore the registry from disk so device dedup (and the assigned computer_id)
	// survive a mock restart — a reinstalled agent then reconciles to its SAME ids.
	srv.loadRegistry()

	// Test helper: pre-seed N dummy devices so a single VM can exercise the
	// license cap (AEGIS_PRESEED_DEVICES). Each consumes a seat.
	if n, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("AEGIS_PRESEED_DEVICES"))); n > 0 {
		for i := 1; i <= n; i++ {
			rec := &deviceRecord{
				DeviceID:   fmt.Sprintf("seed-%d", i),
				SystemUUID: fmt.Sprintf("seed-uuid-%d", i),
				Hostname:   fmt.Sprintf("seed-host-%d", i),
				LastSeen:   time.Now(),
			}
			srv.byID[rec.DeviceID] = rec
			srv.bySysUUID[rec.SystemUUID] = rec
		}
		mockPrintf("Pre-seeded %d dummy device(s) for license testing\n", n)
	}

	// 150 MB max message size: default 4 MB is too small for file_operation
	// write_bytes with inline Content up to AEGIS_FILE_WRITE_MAX_BYTES
	// (100 MB default) + JSON base64 overhead (~33%). Streaming RPCs
	// (ShellSession, FetchContent, UploadContent) are not affected because
	// they chunk, but the signed Command Payload is a single message so
	// its ceiling matters.
	const maxMsgBytes = 150 * 1024 * 1024
	s := grpc.NewServer(
		grpc.Creds(creds),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             20 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.MaxRecvMsgSize(maxMsgBytes),
		grpc.MaxSendMsgSize(maxMsgBytes),
	)
	pb.RegisterAgentControlPlaneServer(s, srv)
	mockPrintf("Mock server listening on :50051 (single port: mTLS ops + enrollment; token=%s)\n", maskToken(os.Getenv("AEGIS_ENROLL_TOKEN")))

	// NSH REPL: start the console operator shell in-process when asked. The real
	// console UI will drive the same engine from a web terminal; here the REPL
	// uses the mockserver's own stdin for ad-hoc operator work and script runs.
	if mode.nsh {
		go runNSH(srv, mode.script)
	} else {
		mockPrintln("Normal server mode: NSH is not running; agent and action logs appear here.")
	}

	if err := s.Serve(lis); err != nil {
		mockFatalf("failed to serve: %v", err)
	}
}

// truthy reports whether an env flag is set to an on-ish value.
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// int64FromEnv parses an integer env var; empty/invalid → 0 (agent defaults apply).
func int64FromEnv(name string) int64 {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// parseParams turns "k1=v1,k2=v2" into a map for SoftwareDeploySpec.parameters
// (test harness for ${name} substitution). Empty input → nil.
func parseParams(v string) map[string]string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	m := map[string]string{}
	for _, pair := range strings.Split(v, ",") {
		if k, val, ok := strings.Cut(pair, "="); ok {
			if k = strings.TrimSpace(k); k != "" {
				m[k] = val
			}
		}
	}
	return m
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
