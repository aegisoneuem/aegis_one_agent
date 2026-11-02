package softdist

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsAuditedScript(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"", false},
		{"C:\\tmp\\install.msi", false},
		{"C:\\tmp\\install.exe", false},
		{"C:\\tmp\\bundle.zip", false},
		{"C:\\tmp\\go.PS1", true},
		{"C:\\tmp\\go.ps1", true},
		{"C:\\tmp\\go.bat", true},
		{"C:\\tmp\\go.cmd", true},
		{"C:\\tmp\\go.vbs", true},
		{"C:\\tmp\\go.py", true},
	}
	for _, c := range cases {
		if got := IsAuditedScript(c.path); got != c.want {
			t.Errorf("IsAuditedScript(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestResolveScriptAuditPolicy(t *testing.T) {
	cases := []struct {
		env      string
		spec     bool
		wantPol  ScriptAuditPolicy
		wantFull bool
	}{
		{"", true, ScriptAuditPolicyEnabled, true},
		{"", false, ScriptAuditPolicyEnabled, false},
		{"enabled", true, ScriptAuditPolicyEnabled, true},
		{"ENABLED", false, ScriptAuditPolicyEnabled, false},
		{"disabled", true, ScriptAuditPolicyDisabled, false},   // kill switch beats signed flag
		{"Disabled", true, ScriptAuditPolicyDisabled, false},   // case-insensitive
		{"always", false, ScriptAuditPolicyAlways, true},       // force-on beats false flag
		{"ALWAYS", false, ScriptAuditPolicyAlways, true},
		{"garbage", true, ScriptAuditPolicyEnabled, true},      // unknown falls back to enabled
	}
	for _, c := range cases {
		pol, full := ResolveScriptAuditPolicy(c.env, c.spec)
		if pol != c.wantPol || full != c.wantFull {
			t.Errorf("ResolveScriptAuditPolicy(%q,%v) = (%v,%v), want (%v,%v)", c.env, c.spec, pol, full, c.wantPol, c.wantFull)
		}
	}
}

func TestAuditScript_SmallFile(t *testing.T) {
	content := []byte("echo hello\r\nexit 0\r\n")
	tmp := t.TempDir()
	path := filepath.Join(tmp, "small.ps1")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := AuditScript("act-1", path, false)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(content)
	if rec.SHA256 != hex.EncodeToString(wantHash[:]) {
		t.Errorf("SHA256 mismatch: got %s", rec.SHA256)
	}
	if rec.Size != int64(len(content)) {
		t.Errorf("size = %d, want %d", rec.Size, len(content))
	}
	if string(rec.Head) != string(content) {
		t.Errorf("head != content: head=%q", string(rec.Head))
	}
	if string(rec.Tail) != string(content) {
		t.Errorf("tail != content for small script")
	}
	if rec.FullCapture || rec.Full != nil {
		t.Errorf("Full should be empty when captureFull=false")
	}
}

func TestAuditScript_LargeFile_HeadTail(t *testing.T) {
	// 2 KiB — larger than 2 * ScriptAuditPreviewBytes (1000) so head and
	// tail are distinct and the middle is truly discarded.
	content := make([]byte, 2048)
	for i := range content {
		content[i] = byte('a' + (i % 26))
	}
	tmp := t.TempDir()
	path := filepath.Join(tmp, "big.ps1")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := AuditScript("act-2", path, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Head) != ScriptAuditPreviewBytes {
		t.Errorf("head size = %d, want %d", len(rec.Head), ScriptAuditPreviewBytes)
	}
	if len(rec.Tail) != ScriptAuditPreviewBytes {
		t.Errorf("tail size = %d, want %d", len(rec.Tail), ScriptAuditPreviewBytes)
	}
	if string(rec.Head) != string(content[:ScriptAuditPreviewBytes]) {
		t.Errorf("head is not the first %d bytes", ScriptAuditPreviewBytes)
	}
	if string(rec.Tail) != string(content[len(content)-ScriptAuditPreviewBytes:]) {
		t.Errorf("tail is not the last %d bytes", ScriptAuditPreviewBytes)
	}
	if !rec.FullCapture || len(rec.Full) != len(content) {
		t.Errorf("FullCapture=%v, Full len=%d, want true and %d", rec.FullCapture, len(rec.Full), len(content))
	}
}

func TestWriteScriptAuditTranscript_ModeAndContent(t *testing.T) {
	tmp := t.TempDir()
	rec := &ScriptAuditRecord{
		ActionID:    "act-9",
		ScriptPath:  "C:\\tmp\\ignored.ps1",
		SHA256:      "abc",
		Size:        12,
		Head:        []byte("echo hello\r\n"),
		Tail:        []byte("echo hello\r\n"),
		Policy:      ScriptAuditPolicyEnabled,
		TaskFlag:    false,
		FullCapture: false,
	}
	path, err := WriteScriptAuditTranscript(tmp, "act-9", rec)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "Action ID: act-9") || !strings.Contains(s, "SHA-256: abc") {
		t.Errorf("header missing expected fields:\n%s", s)
	}
	if !strings.Contains(s, "echo hello") {
		t.Error("transcript missing script preview")
	}
	if strings.Contains(s, "FULL CONTENT") {
		t.Error("transcript should NOT have FULL CONTENT section when FullCapture=false")
	}
	// Full-content test
	recFull := &ScriptAuditRecord{
		ActionID: "act-full", ScriptPath: "x", SHA256: "x", Size: 5,
		Head: []byte("hello"), Tail: []byte("hello"),
		Full: []byte("hello"), FullCapture: true,
	}
	path2, err := WriteScriptAuditTranscript(tmp, "act-full", recFull)
	if err != nil {
		t.Fatal(err)
	}
	data2, _ := os.ReadFile(path2)
	if !strings.Contains(string(data2), "FULL CONTENT") {
		t.Error("transcript should have FULL CONTENT section when FullCapture=true")
	}
}

func TestSanitizeTranscriptName(t *testing.T) {
	cases := map[string]string{
		"nsh-abc123":  "nsh-abc123",
		"abc/def":     "abc_def",
		"a..\\b":      "a.._b",
		"OK_name.1":   "OK_name.1",
		"with space":  "with_space",
	}
	for in, want := range cases {
		if got := sanitizeTranscriptName(in); got != want {
			t.Errorf("sanitizeTranscriptName(%q) = %q, want %q", in, got, want)
		}
	}
}
