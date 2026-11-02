package softdist

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ScriptAuditPreviewBytes is the head/tail window size for the default audit
// record (SHA-256 + size + first N + last N bytes). 500 is small enough to
// log cheaply yet large enough to recognize a vendor's actual script header
// in reviews. The constant is here so Vijay can tune it from one place.
const ScriptAuditPreviewBytes = 500

// ScriptAuditPolicy — endpoint policy for how much of a script to log.
// Set via AEGIS_LOG_SCRIPT_CONTENT and resolved by ResolveScriptAuditPolicy.
type ScriptAuditPolicy string

const (
	// ScriptAuditPolicyEnabled honors the per-task CaptureScriptContent
	// flag: FALSE → hash+preview only, TRUE → full content.
	ScriptAuditPolicyEnabled ScriptAuditPolicy = "enabled"
	// ScriptAuditPolicyDisabled forces hash+preview only regardless of the
	// per-task flag (regulated endpoints where script content must NEVER
	// land on disk). This is a kill switch — the server's signed flag
	// cannot override endpoint policy.
	ScriptAuditPolicyDisabled ScriptAuditPolicy = "disabled"
	// ScriptAuditPolicyAlways forces full content on every script
	// regardless of the per-task flag (compliance / forensic endpoints
	// where every scripted install must be fully captured).
	ScriptAuditPolicyAlways ScriptAuditPolicy = "always"
)

// scriptExtensions is the fixed set of extensions audited. MSI/EXE binaries
// are not here (hashing + logging binary bytes is pointless for audit; their
// SHA-256 is already captured elsewhere in the install flow).
var scriptExtensions = map[string]bool{
	".ps1": true, ".bat": true, ".cmd": true, ".vbs": true, ".py": true,
}

// IsAuditedScript returns true when filePath ends in one of the audited
// script extensions (case-insensitive). Returns false for empty paths, MSI,
// EXE, ZIP (bundle), and any other extension — the audit is specifically for
// script payloads that would otherwise execute without content visibility.
func IsAuditedScript(filePath string) bool {
	if strings.TrimSpace(filePath) == "" {
		return false
	}
	return scriptExtensions[strings.ToLower(filepath.Ext(filePath))]
}

// ScriptAuditRecord is the per-script audit record written to the softdist
// transcript. SHA256, Size, Head, Tail are populated on every audit; Full
// is populated only when the resolved policy says to capture full content.
// SecureParameters are NEVER substituted into any of these fields — scripts
// are read verbatim from disk before any parameter substitution.
type ScriptAuditRecord struct {
	ActionID    string
	ScriptPath  string
	SHA256      string
	Size        int64
	Head        []byte
	Tail        []byte
	Full        []byte
	FullCapture bool // true when Full is populated (for the transcript header)
	Policy      ScriptAuditPolicy
	TaskFlag    bool // the per-task CaptureScriptContent flag that was signed
}

// AuditScript reads the file at filePath and returns a tamper-evident audit
// record. captureFull controls whether the record carries the FULL content
// (compute from ResolveScriptAuditPolicy, do not pass the per-task flag
// directly — the endpoint policy wins).
//
// Returns an error if the file cannot be read; the caller is expected to
// propagate the failure as an installer failure (fail-loud: if we cannot
// audit what we are about to run, we must not run it blindly).
func AuditScript(actionID, filePath string, captureFull bool) (*ScriptAuditRecord, error) {
	if strings.TrimSpace(filePath) == "" {
		return nil, errors.New("audit script: empty file path")
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("audit script: read %s: %w", filePath, err)
	}
	sum := sha256.Sum256(content)
	rec := &ScriptAuditRecord{
		ActionID:   actionID,
		ScriptPath: filePath,
		SHA256:     hex.EncodeToString(sum[:]),
		Size:       int64(len(content)),
	}
	if len(content) <= ScriptAuditPreviewBytes {
		// Small script: head and tail are the whole thing. Copy to keep
		// Head/Tail independently addressable (so a later edit to one does
		// not affect the other via shared backing slice).
		rec.Head = append([]byte(nil), content...)
		rec.Tail = append([]byte(nil), content...)
	} else {
		rec.Head = append([]byte(nil), content[:ScriptAuditPreviewBytes]...)
		rec.Tail = append([]byte(nil), content[len(content)-ScriptAuditPreviewBytes:]...)
	}
	if captureFull {
		rec.Full = append([]byte(nil), content...)
		rec.FullCapture = true
	}
	return rec, nil
}

// ResolveScriptAuditPolicy folds the endpoint policy (from
// AEGIS_LOG_SCRIPT_CONTENT) with the per-task CaptureScriptContent flag into
// a single effective "capture full content" bool. Endpoint policy wins:
//   - policy = disabled → always false (kill switch; signed flag ignored)
//   - policy = always   → always true  (force-on; signed flag ignored)
//   - policy = enabled or empty → honor the per-task flag
//
// An unrecognized policy value is treated as "enabled" (fail-open to the
// signed flag) and the caller may want to warn-log that the config was
// misspelled; this function does not log.
func ResolveScriptAuditPolicy(envValue string, specFlag bool) (ScriptAuditPolicy, bool) {
	p := ScriptAuditPolicy(strings.ToLower(strings.TrimSpace(envValue)))
	switch p {
	case ScriptAuditPolicyDisabled:
		return ScriptAuditPolicyDisabled, false
	case ScriptAuditPolicyAlways:
		return ScriptAuditPolicyAlways, true
	case ScriptAuditPolicyEnabled, "":
		return ScriptAuditPolicyEnabled, specFlag
	default:
		return ScriptAuditPolicyEnabled, specFlag
	}
}

// WriteScriptAuditTranscript writes rec to
// <skvcLogsDir>/softdist/<actionID>.transcript with mode 0600 and returns the
// written path. Directory is created with mode 0700. The file is opened with
// O_WRONLY|O_CREATE|O_TRUNC so a retry overwrites the previous record for
// the same action.
//
// SecureParameters are NEVER part of rec and NEVER passed to this function —
// the content captured here is the raw script bytes before any substitution,
// so a secret value delivered via secure stdin cannot leak through this path.
func WriteScriptAuditTranscript(skvcLogsDir, actionID string, rec *ScriptAuditRecord) (string, error) {
	if strings.TrimSpace(actionID) == "" {
		return "", errors.New("write script audit transcript: empty action id")
	}
	if rec == nil {
		return "", errors.New("write script audit transcript: nil record")
	}
	dir := filepath.Join(skvcLogsDir, "softdist")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("write script audit transcript: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, sanitizeTranscriptName(actionID)+".transcript")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("write script audit transcript: open %s: %w", path, err)
	}
	defer f.Close()
	header := fmt.Sprintf(`# Aegis One SoftDist script audit transcript
# Written: %s
# Action ID: %s
# Script path: %s
# Script size (bytes): %d
# SHA-256: %s
# Endpoint policy: %s
# Per-task CaptureScriptContent flag: %t
# Full content captured: %t

# ------ HEAD (first %d bytes, verbatim) ------
`, time.Now().UTC().Format(time.RFC3339), rec.ActionID, rec.ScriptPath, rec.Size, rec.SHA256, rec.Policy, rec.TaskFlag, rec.FullCapture, ScriptAuditPreviewBytes)
	if _, err := f.WriteString(header); err != nil {
		return "", fmt.Errorf("write script audit transcript: header: %w", err)
	}
	if _, err := f.Write(rec.Head); err != nil {
		return "", fmt.Errorf("write script audit transcript: head: %w", err)
	}
	if _, err := f.WriteString(fmt.Sprintf("\n\n# ------ TAIL (last %d bytes, verbatim) ------\n", ScriptAuditPreviewBytes)); err != nil {
		return "", fmt.Errorf("write script audit transcript: tail header: %w", err)
	}
	if _, err := f.Write(rec.Tail); err != nil {
		return "", fmt.Errorf("write script audit transcript: tail: %w", err)
	}
	if rec.FullCapture && len(rec.Full) > 0 {
		if _, err := f.WriteString("\n\n# ------ FULL CONTENT (verbatim, policy-opt-in) ------\n"); err != nil {
			return "", fmt.Errorf("write script audit transcript: full header: %w", err)
		}
		if _, err := f.Write(rec.Full); err != nil {
			return "", fmt.Errorf("write script audit transcript: full: %w", err)
		}
	}
	if _, err := f.WriteString("\n"); err != nil {
		return "", fmt.Errorf("write script audit transcript: trailing newline: %w", err)
	}
	return path, nil
}

// sanitizeTranscriptName strips characters that would be unsafe in a Windows
// filename. action IDs are expected to be short IDs ("nsh-<hex>", GUIDs,
// numeric strings) so this is defensive, not a general-purpose sanitizer.
func sanitizeTranscriptName(actionID string) string {
	var b strings.Builder
	b.Grow(len(actionID))
	for _, r := range actionID {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
