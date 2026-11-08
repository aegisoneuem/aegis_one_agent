package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// resumeTestServer is a FetchContent server that honors the resume Offset and can
// simulate a mid-transfer disconnect on the first attempt, or serve corrupt bytes,
// so the client-side checkpoint-restart loop can be exercised over the REAL gRPC
// path (bufconn), not a stub.
type resumeTestServer struct {
	pb.UnimplementedAgentControlPlaneServer
	data           []byte
	corrupt        bool  // serve data with a flipped byte so the SHA never matches
	failFirstAfter int   // >0: on attempt 1, send this many bytes then disconnect
	blockAfter     int   // >0: on attempt 1, send this many bytes then block until the client cancels
	mu             sync.Mutex
	attempts       int
	offsets        []int64 // the Offset seen on each attempt, in order
}

func (s *resumeTestServer) FetchContent(req *pb.ContentRequest, stream pb.AgentControlPlane_FetchContentServer) error {
	s.mu.Lock()
	s.attempts++
	attempt := s.attempts
	s.offsets = append(s.offsets, req.Offset)
	s.mu.Unlock()

	start := int(req.Offset)
	if start > len(s.data) {
		return status.Errorf(codes.OutOfRange, "offset %d past end of %d bytes", start, len(s.data))
	}
	payload := append([]byte(nil), s.data[start:]...)
	if s.corrupt && len(payload) > 0 {
		payload[0] ^= 0xFF // one flipped byte -> whole-file SHA cannot match
	}
	limit := len(payload)
	if attempt == 1 && s.failFirstAfter > 0 && s.failFirstAfter < limit {
		limit = s.failFirstAfter
	}
	if attempt == 1 && s.blockAfter > 0 && s.blockAfter < limit {
		limit = s.blockAfter
	}
	const chunk = 4096
	for off := 0; off < limit; off += chunk {
		end := off + chunk
		if end > limit {
			end = limit
		}
		if err := stream.Send(&pb.ContentChunk{Data: payload[off:end]}); err != nil {
			return err
		}
	}
	if attempt == 1 && s.blockAfter > 0 {
		// Simulate a stuck transfer: hold the stream open until the client's
		// context is cancelled (the real "server went away" case cancels the
		// per-connection context on the agent side).
		<-stream.Context().Done()
		return stream.Context().Err()
	}
	if attempt == 1 && s.failFirstAfter > 0 {
		return status.Error(codes.Unavailable, "simulated mid-transfer disconnect")
	}
	return nil
}

// newResumeClient spins the test server on an in-memory listener and returns a
// real gRPC client for it.
func newResumeClient(t *testing.T, srv *resumeTestServer) pb.AgentControlPlaneClient {
	t.Helper()
	listener := bufconn.Listen(2 << 20)
	server := grpc.NewServer()
	pb.RegisterAgentControlPlaneServer(server, srv)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///fetch-resume",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return pb.NewAgentControlPlaneClient(conn)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	r := rand.New(rand.NewSource(1)) // deterministic for reproducibility
	r.Read(b)
	return b
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// fastBackoff shortens the retry pause for the duration of a test.
func fastBackoff(t *testing.T) {
	t.Helper()
	savedBackoff, savedAttempts := fetchRetryBackoff, fetchMaxAttempts
	fetchRetryBackoff = time.Millisecond
	t.Cleanup(func() { fetchRetryBackoff, fetchMaxAttempts = savedBackoff, savedAttempts })
}

// TestFetchContent_ResumesFromOffsetAfterDisconnect is the core checkpoint-restart
// case: the first attempt disconnects partway, and the second attempt must resume
// from the bytes already on disk (NOT from zero) and complete a byte-identical file.
func TestFetchContent_ResumesFromOffsetAfterDisconnect(t *testing.T) {
	fastBackoff(t)
	data := randomBytes(100_000)
	srv := &resumeTestServer{data: data, failFirstAfter: 40_000}
	client := newResumeClient(t, srv)

	dest := filepath.Join(t.TempDir(), "pkg.bin")
	req := &pb.ContentRequest{Kind: "package", Id: "resume-1", Sha256: sha256Hex(data)}
	if err := fetchContent(context.Background(), client, req, dest, 0); err != nil {
		t.Fatalf("fetchContent: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if sha256Hex(got) != sha256Hex(data) || len(got) != len(data) {
		t.Fatalf("resumed file differs: got %d bytes", len(got))
	}
	if srv.attempts != 2 {
		t.Fatalf("expected 2 attempts (disconnect + resume), got %d", srv.attempts)
	}
	// The whole point: the second attempt resumed from the partial offset, not 0.
	if len(srv.offsets) != 2 || srv.offsets[0] != 0 || srv.offsets[1] != 40_000 {
		t.Fatalf("expected offsets [0 40000], got %v", srv.offsets)
	}
	// Partial must be cleaned up after success.
	if _, err := os.Stat(fetchPartialPath(dest, req)); !os.IsNotExist(err) {
		t.Fatalf("partial file left behind after success")
	}
}

// TestFetchContent_ResumesAcrossSeparateCallsAfterCancel reproduces the REAL VM
// scenario: the server disconnects, which cancels the agent's per-connection
// context, so the first fetchContent call returns (deploy fails). The server then
// re-issues the command, and a SECOND, independent fetchContent call must resume
// from the partial the first call left on disk — NOT re-download from zero.
func TestFetchContent_ResumesAcrossSeparateCallsAfterCancel(t *testing.T) {
	fastBackoff(t)
	data := randomBytes(100_000)
	srv := &resumeTestServer{data: data, blockAfter: 40_000}
	client := newResumeClient(t, srv)

	dir := t.TempDir()
	dest := filepath.Join(dir, "pkg.bin")
	req := &pb.ContentRequest{Kind: "package", Id: "resume-x", Sha256: sha256Hex(data)}

	// First call: cancel the context once the server has sent the first ~40 KB,
	// mimicking the connection drop that cancels the agent's context mid-transfer.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(250 * time.Millisecond)
		cancel()
	}()
	if err := fetchContent(ctx, client, req, dest, 0); err == nil {
		t.Fatal("first call should fail on cancel")
	}

	// The partial must survive the cancelled call so the re-issue can resume it.
	partial := fetchPartialPath(dest, req)
	info, serr := os.Stat(partial)
	if serr != nil {
		t.Fatalf("partial not kept after cancel: %v", serr)
	}
	if info.Size() == 0 {
		t.Fatal("partial kept but empty — nothing to resume from")
	}
	kept := info.Size()

	// Second call: a fresh context (the reconnect). It must resume from the kept
	// bytes, so the server sees a non-zero offset, and produce a correct file.
	if err := fetchContent(context.Background(), client, req, dest, 0); err != nil {
		t.Fatalf("resume call failed: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || sha256Hex(got) != sha256Hex(data) {
		t.Fatal("resumed file must match source exactly")
	}
	srv.mu.Lock()
	lastOffset := srv.offsets[len(srv.offsets)-1]
	srv.mu.Unlock()
	if lastOffset == 0 {
		t.Fatalf("re-issued fetch restarted from zero; expected resume from ~%d", kept)
	}
}

// TestFetchContent_SingleShotSucceeds proves the happy path still works and asks
// for offset 0 exactly once.
func TestFetchContent_SingleShotSucceeds(t *testing.T) {
	fastBackoff(t)
	data := randomBytes(20_000)
	srv := &resumeTestServer{data: data}
	client := newResumeClient(t, srv)

	dest := filepath.Join(t.TempDir(), "pkg.bin")
	req := &pb.ContentRequest{Kind: "package", Id: "single", Sha256: sha256Hex(data)}
	if err := fetchContent(context.Background(), client, req, dest, 0); err != nil {
		t.Fatalf("fetchContent: %v", err)
	}
	if srv.attempts != 1 || len(srv.offsets) != 1 || srv.offsets[0] != 0 {
		t.Fatalf("expected one attempt at offset 0, got attempts=%d offsets=%v", srv.attempts, srv.offsets)
	}
}

// TestFetchContent_CorruptContentDiscardsPartialAndFails is a break test: a
// completed-but-wrong transfer must NOT be accepted (SHA guard), the bad partial
// must be discarded (not resumed), and after exhausting attempts the call fails
// loud with no file left on disk.
func TestFetchContent_CorruptContentDiscardsPartialAndFails(t *testing.T) {
	fastBackoff(t)
	fetchMaxAttempts = 3
	data := randomBytes(10_000)
	srv := &resumeTestServer{data: data, corrupt: true}
	client := newResumeClient(t, srv)

	dest := filepath.Join(t.TempDir(), "pkg.bin")
	req := &pb.ContentRequest{Kind: "package", Id: "corrupt", Sha256: sha256Hex(data)}
	err := fetchContent(context.Background(), client, req, dest, 0)
	if err == nil {
		t.Fatal("expected failure on persistently corrupt content")
	}
	// Every attempt restarts from zero (corrupt partial is discarded, not resumed).
	for i, off := range srv.offsets {
		if off != 0 {
			t.Fatalf("corrupt retry %d resumed from %d, expected restart at 0", i, off)
		}
	}
	if srv.attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", srv.attempts)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("corrupt content must not land at destination")
	}
	if _, err := os.Stat(fetchPartialPath(dest, req)); !os.IsNotExist(err) {
		t.Fatal("corrupt partial must be removed")
	}
}

// TestFetchContent_ContextCancelStops is a break test: a cancelled context must
// stop the fetch promptly without retry churn and must not leave a partial.
func TestFetchContent_ContextCancelStops(t *testing.T) {
	fastBackoff(t)
	data := randomBytes(50_000)
	srv := &resumeTestServer{data: data, failFirstAfter: 10_000}
	client := newResumeClient(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	dest := filepath.Join(t.TempDir(), "pkg.bin")
	req := &pb.ContentRequest{Kind: "package", Id: "cancel", Sha256: sha256Hex(data)}
	err := fetchContent(ctx, client, req, dest, 0)
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
	if _, err := os.Stat(fetchPartialPath(dest, req)); !os.IsNotExist(err) {
		t.Fatal("partial must be removed after cancel")
	}
}

// TestFetchContent_StalePartialPastEndSelfHeals is a break test: a leftover partial
// LARGER than the real file (resume offset past the source end) must not wedge the
// transfer forever. The server rejects the offset (OutOfRange); the agent discards
// the stale partial and restarts from zero, producing a correct file.
func TestFetchContent_StalePartialPastEndSelfHeals(t *testing.T) {
	fastBackoff(t)
	data := randomBytes(5_000)
	srv := &resumeTestServer{data: data}
	client := newResumeClient(t, srv)

	dest := filepath.Join(t.TempDir(), "pkg.bin")
	req := &pb.ContentRequest{Kind: "package", Id: "stale", Sha256: sha256Hex(data)}
	// Plant a stale partial bigger than the real content.
	partial := fetchPartialPath(dest, req)
	if err := os.WriteFile(partial, randomBytes(9_000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fetchContent(context.Background(), client, req, dest, 0); err != nil {
		t.Fatalf("expected self-heal to succeed, got: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || sha256Hex(got) != sha256Hex(data) {
		t.Fatal("self-healed file must match the source exactly")
	}
	// First attempt hit the stale offset (9000), second restarted from zero.
	if len(srv.offsets) < 2 || srv.offsets[len(srv.offsets)-1] != 0 {
		t.Fatalf("expected a restart at offset 0 after the stale offset, got %v", srv.offsets)
	}
}

func TestFetchPartialPath_KeyedByContent(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "sub", "out.bin")
	// Same SHA -> same partial path regardless of dest filename, so a re-fetch of
	// the same content resumes.
	a := fetchPartialPath(dest, &pb.ContentRequest{Id: "x", Sha256: "ABCDEF0123"})
	b := fetchPartialPath(filepath.Join(dir, "sub", "other.bin"), &pb.ContentRequest{Id: "y", Sha256: "ABCDEF0123"})
	if a != b {
		t.Fatalf("same SHA should map to same partial: %q vs %q", a, b)
	}
	if filepath.Dir(a) != filepath.Join(dir, "sub") {
		t.Fatalf("partial should sit next to dest, got %q", a)
	}
	// Falls back to id when no SHA.
	if got := fetchPartialPath(dest, &pb.ContentRequest{Id: "only-id"}); got == "" {
		t.Fatal("expected a partial path from id fallback")
	}
}

func TestSanitizeFetchKey(t *testing.T) {
	if got := sanitizeFetchKey("ab/cd\\ef*g?"); got != "ab_cd_ef_g_" {
		t.Fatalf("sanitizeFetchKey = %q", got)
	}
	if got := sanitizeFetchKey("Good-1.0_x"); got != "Good-1.0_x" {
		t.Fatalf("safe chars changed: %q", got)
	}
}

// guard so the errFetchCorruptPartial sentinel stays wired (used by fetchContent).
func TestErrFetchCorruptPartialSentinel(t *testing.T) {
	wrapped := errors.New("x")
	if errors.Is(wrapped, errFetchCorruptPartial) {
		t.Fatal("unrelated error must not match the sentinel")
	}
}
