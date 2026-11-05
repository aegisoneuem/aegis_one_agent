package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aegisoneuem/aegis_one_agent/internal/fileops"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

// L1.7 — one test per command type registered in dispatch.handlers.
// Each test confirms the summary includes the key identity fields an
// auditor needs to say WHAT was done, and that no secret values appear
// when none were supplied.

func TestPayloadSummary_RunCommand(t *testing.T) {
	spec := &pb.RunCommandSpec{Shell: "cmd", Command: "dir C:\\"}
	p, _ := proto.Marshal(spec)
	cmd := &pb.Command{Type: "run_command", Payload: p}
	got := payloadSummary(cmd)
	if !strings.Contains(got, "shell=cmd") || !strings.Contains(got, "command=dir C:\\") {
		t.Errorf("got %q, want shell+command visible", got)
	}
}

func TestPayloadSummary_InstallPatch(t *testing.T) {
	spec := &pb.InstallPatchSpec{Kb: "KB5126052"}
	p, _ := proto.Marshal(spec)
	cmd := &pb.Command{Type: "install_patch", Payload: p}
	got := payloadSummary(cmd)
	if got != "kb=KB5126052" {
		t.Errorf("got %q, want %q", got, "kb=KB5126052")
	}
}

func TestPayloadSummary_SoftwareDeploy_NoSecrets(t *testing.T) {
	spec := &pb.SoftwareDeploySpec{Name: "Google Chrome", Version: "145.0", Action: "install"}
	p, _ := proto.Marshal(spec)
	cmd := &pb.Command{Type: "software_deploy", Payload: p}
	got := payloadSummary(cmd)
	if !strings.Contains(got, "Google Chrome") || !strings.Contains(got, "145.0") || !strings.Contains(got, "install") {
		t.Errorf("got %q, want name/version/action visible", got)
	}
}

// L1.8 — Secret guard: if SecureParameters are set, the summary MUST
// NOT echo product name / version / args — only the fixed sensitive
// marker.
func TestPayloadSummary_SoftwareDeploy_SecretGuard(t *testing.T) {
	spec := &pb.SoftwareDeploySpec{
		Name:    "Internal App",
		Version: "1.0",
		Action:  "install",
		SecureParameters: map[string]string{
			"DB_PASSWORD": "p@ssw0rd-should-never-leak",
		},
	}
	p, _ := proto.Marshal(spec)
	cmd := &pb.Command{Type: "software_deploy_v2", Payload: p}
	got := payloadSummary(cmd)
	if got != auditSensitiveMarker {
		t.Errorf("got %q, want sensitive marker — summary MUST NOT leak when secrets are present", got)
	}
	if strings.Contains(got, "p@ssw0rd") {
		t.Errorf("CRITICAL: secret leaked into audit summary: %q", got)
	}
	if strings.Contains(got, "Internal App") {
		t.Errorf("product name leaked despite sensitive marker: %q", got)
	}
}

func TestPayloadSummary_SoftwareDeploy_SecretGuardViaExecutionPassword(t *testing.T) {
	spec := &pb.SoftwareDeploySpec{
		Name:    "Internal App",
		Version: "1.0",
		Action:  "install",
		Execution: &pb.SoftwareExecution{
			Mode:         "specified_account",
			Username:     "svc-account",
			PasswordUtf8: []byte("hunter2"),
		},
	}
	p, _ := proto.Marshal(spec)
	cmd := &pb.Command{Type: "software_deploy_v2", Payload: p}
	got := payloadSummary(cmd)
	if got != auditSensitiveMarker {
		t.Errorf("got %q, want sensitive marker when execution password present", got)
	}
	if strings.Contains(got, "hunter2") {
		t.Errorf("CRITICAL: execution password leaked into audit summary: %q", got)
	}
}

func TestPayloadSummary_CollectFile(t *testing.T) {
	spec := &pb.CollectFileSpec{Paths: []string{"C:\\log1.txt", "C:\\log2.txt"}}
	p, _ := proto.Marshal(spec)
	cmd := &pb.Command{Type: "collect_file", Payload: p}
	got := payloadSummary(cmd)
	if !strings.Contains(got, "paths=2") || !strings.Contains(got, "first=C:\\log1.txt") {
		t.Errorf("got %q, want paths=2 and first=... visible", got)
	}
}

func TestPayloadSummary_FileOperation(t *testing.T) {
	spec := fileops.Spec{Version: 1, Operation: "write_bytes", Destination: "C:\\tmp\\x.txt"}
	p, _ := json.Marshal(spec)
	cmd := &pb.Command{Type: "file_operation", Payload: p}
	got := payloadSummary(cmd)
	if !strings.Contains(got, "op=write_bytes") || !strings.Contains(got, "dst=C:\\tmp\\x.txt") {
		t.Errorf("got %q, want op+dst visible", got)
	}
}

func TestPayloadSummary_Reboot(t *testing.T) {
	spec := &pb.RebootSpec{DelaySeconds: 60}
	p, _ := proto.Marshal(spec)
	cmd := &pb.Command{Type: "reboot", Payload: p}
	got := payloadSummary(cmd)
	if got != "delay=60s" {
		t.Errorf("got %q, want delay=60s", got)
	}
}

func TestPayloadSummary_OpenShell(t *testing.T) {
	spec := &pb.OpenShellSpec{SessionId: "0123456789abcdef-long-session-id"}
	p, _ := proto.Marshal(spec)
	cmd := &pb.Command{Type: "open_shell", Payload: p}
	got := payloadSummary(cmd)
	if !strings.HasPrefix(got, "session=") {
		t.Errorf("got %q, want session=... prefix", got)
	}
	if strings.Contains(got, "long-session-id") {
		t.Error("full session id leaked; should truncate")
	}
}

func TestPayloadSummary_SimpleTypes_Empty(t *testing.T) {
	for _, typ := range []string{"scan_now", "scan", "report_device_state", "cancel_reboot"} {
		cmd := &pb.Command{Type: typ}
		got := payloadSummary(cmd)
		if got != "" {
			t.Errorf("type %q got %q, want empty", typ, got)
		}
	}
}

func TestPayloadSummary_UnknownType(t *testing.T) {
	cmd := &pb.Command{Type: "something_the_agent_doesnt_know_yet"}
	got := payloadSummary(cmd)
	if got != "" {
		t.Errorf("got %q, want empty for unknown type", got)
	}
}

func TestPayloadSummary_NilCommand(t *testing.T) {
	got := payloadSummary(nil)
	if got != "" {
		t.Errorf("got %q, want empty for nil command", got)
	}
}

// L1.10 BREAK tests ------------------------------------------------

// BREAK 1: Malformed payload (type says run_command but payload bytes
// are random). Should return "<unparseable payload>" without crashing.
func TestPayloadSummary_Break_MalformedPayload(t *testing.T) {
	cmd := &pb.Command{Type: "run_command", Payload: []byte{0xde, 0xad, 0xbe, 0xef}}
	got := payloadSummary(cmd)
	if got != "<unparseable payload>" {
		t.Errorf("got %q, want fallback marker on malformed payload", got)
	}
}

// BREAK 2: Empty payload. Same handling.
func TestPayloadSummary_Break_EmptyPayload(t *testing.T) {
	// Empty proto bytes unmarshal to a zero-value spec — valid for run_command.
	// Check behavior stays sane.
	cmd := &pb.Command{Type: "run_command", Payload: []byte{}}
	got := payloadSummary(cmd)
	// Zero-value spec → shell="", command="". Still returns a summary.
	if !strings.HasPrefix(got, "shell=") {
		t.Errorf("got %q, want shell= prefix even on empty payload", got)
	}
}

// BREAK 3: Huge run_command text. Must truncate to auditDetailsMax.
func TestPayloadSummary_Break_HugeCommand(t *testing.T) {
	huge := strings.Repeat("A", 5000)
	spec := &pb.RunCommandSpec{Shell: "cmd", Command: huge}
	p, _ := proto.Marshal(spec)
	cmd := &pb.Command{Type: "run_command", Payload: p}
	got := payloadSummary(cmd)
	if len(got) > auditDetailsMax {
		t.Errorf("summary length = %d, want <= %d (truncation failed)", len(got), auditDetailsMax)
	}
	if !strings.HasSuffix(got, "(truncated)") {
		t.Errorf("huge payload should end with truncation marker; got %q", got)
	}
}

// BREAK 4: Binary payload in file_operation Content. Should NOT dump
// the raw bytes into the audit summary (ops description only).
func TestPayloadSummary_Break_BinaryContentNotEchoed(t *testing.T) {
	spec := fileops.Spec{
		Version: 1, Operation: "write_bytes",
		Destination: "C:\\tmp\\binary.bin",
		Content:     make([]byte, 10_000_000), // 10 MB binary payload
	}
	p, _ := json.Marshal(spec)
	cmd := &pb.Command{Type: "file_operation", Payload: p}
	got := payloadSummary(cmd)
	if len(got) > auditDetailsMax {
		t.Errorf("summary length = %d — binary content leaked into audit", len(got))
	}
	// The binary bytes are all 0x00 base64-encoded to "AAAA..." — if any of
	// that string appears in the summary, we've leaked payload bytes.
	if strings.Contains(got, "AAAAAAAA") {
		t.Errorf("binary payload bytes leaked into audit summary: %q", got)
	}
}

// BREAK 5: Case-insensitive type lookup. Admin-provided uppercase type
// should still find the right handler.
func TestPayloadSummary_Break_UppercaseType(t *testing.T) {
	spec := &pb.InstallPatchSpec{Kb: "KB1234567"}
	p, _ := proto.Marshal(spec)
	cmd := &pb.Command{Type: "INSTALL_PATCH", Payload: p}
	got := payloadSummary(cmd)
	if !strings.Contains(got, "KB1234567") {
		t.Errorf("uppercase type not handled: got %q", got)
	}
}
