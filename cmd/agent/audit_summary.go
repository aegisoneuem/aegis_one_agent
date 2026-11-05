package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aegisoneuem/aegis_one_agent/internal/fileops"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

// auditDetailsMax is the hard truncation cap for the audit Details
// string. 400 bytes is deliberately below the 500 B command-text cap we
// log in run_command: accepted so the entire audit event still comfortably
// fits in a sub-1 KB JSON line. Truncation marker is visible to the
// reader (not silent).
const auditDetailsMax = 400

// auditSensitiveMarker is the fixed string we use when a signed command
// carries secure_parameters (or any explicit secret payload). The
// marker never contains the secret or its name — just that it is present
// — so audit records remain informative ("this command used secrets")
// without leaking the material.
const auditSensitiveMarker = "<sensitive — secure parameters in use>"

// payloadSummary extracts a short per-command-type summary string for
// the audit Details field. The function makes decisions for exactly the
// command types registered in dispatch.handlers; unknown types return
// an empty string so the audit record still writes without crashing.
//
// SECURITY: if the signed spec carries secure_parameters, the summary
// is replaced with auditSensitiveMarker so a secret substituted into
// ${name} tokens cannot leak via audit. Literal secrets in script
// sources are the admin's responsibility (same discipline as UC-20
// script content audit).
//
// SAFETY: every branch unmarshals defensively — a malformed payload
// returns "<unparseable payload>" rather than panicking. The audit
// record still writes with the fallback string so an attacker cannot
// use malformed commands to suppress the audit entry.
func payloadSummary(cmd *pb.Command) string {
	if cmd == nil {
		return ""
	}
	raw := trim(payloadSummaryRaw(cmd))
	return truncateAuditDetails(raw)
}

// payloadSummaryRaw is the inner switch — no truncation, no trim.
// Separated so truncation + trim happens in exactly one place.
func payloadSummaryRaw(cmd *pb.Command) string {
	switch strings.ToLower(strings.TrimSpace(cmd.Type)) {

	case "run_command":
		var spec pb.RunCommandSpec
		if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
			return "<unparseable payload>"
		}
		// SecureParameters on RunCommandSpec doesn't exist as a field,
		// but working_dir and parameters can carry sensitive values if
		// the admin misused them. We still log the command text since
		// that is the operator's own input; secrets here are an admin-
		// discipline issue, not a system leak.
		return fmt.Sprintf("shell=%s, command=%s", spec.Shell, spec.Command)

	case "install_patch", "uninstall_patch", "stage_patch":
		var spec pb.InstallPatchSpec
		if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
			return "<unparseable payload>"
		}
		return fmt.Sprintf("kb=%s", spec.Kb)

	case "software_deploy",
		"software_deploy_v2",
		"software_deploy_bundle",
		"software_deploy_workflow",
		"software_deploy_detect":
		var spec pb.SoftwareDeploySpec
		if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
			return "<unparseable payload>"
		}
		// SecureParameters present → redact (do not emit installer
		// arguments or script args that may carry the secret values
		// substituted by the admin before signing).
		if hasSecureParameters(&spec) {
			return auditSensitiveMarker
		}
		return fmt.Sprintf("name=%s, version=%s, action=%s", spec.Name, spec.Version, spec.Action)

	case "collect_file":
		var spec pb.CollectFileSpec
		if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
			return "<unparseable payload>"
		}
		first := ""
		if len(spec.Paths) > 0 {
			first = spec.Paths[0]
		}
		return fmt.Sprintf("paths=%d, first=%s", len(spec.Paths), first)

	case "file_operation":
		var spec fileops.Spec
		if err := json.Unmarshal(cmd.Payload, &spec); err != nil {
			return "<unparseable payload>"
		}
		return fmt.Sprintf("op=%s, src=%s, dst=%s", spec.Operation, spec.Source, spec.Destination)

	case "reboot":
		var spec pb.RebootSpec
		if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
			return "<unparseable payload>"
		}
		return fmt.Sprintf("delay=%ds", spec.DelaySeconds)

	case "cancel_reboot":
		return ""

	case "scan_now", "scan":
		return ""

	case "report_device_state":
		return ""

	case "open_shell":
		var spec pb.OpenShellSpec
		if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
			return "<unparseable payload>"
		}
		sid := spec.SessionId
		if len(sid) > 16 {
			sid = sid[:16] + "…"
		}
		return fmt.Sprintf("session=%s", sid)

	default:
		return ""
	}
}

// hasSecureParameters returns true when the signed deploy spec carries
// ANY secret delivery path the agent treats specially (secure_parameters
// map OR a password on an execution context). Used by payloadSummary to
// decide whether to replace the command summary with the sensitive
// marker.
func hasSecureParameters(spec *pb.SoftwareDeploySpec) bool {
	if spec == nil {
		return false
	}
	if len(spec.SecureParameters) > 0 {
		return true
	}
	if ex := spec.GetExecution(); ex != nil && len(ex.GetPasswordUtf8()) > 0 {
		return true
	}
	return false
}

// trim strips leading/trailing whitespace. Separate helper so
// payloadSummary stays testable without pulling strings.TrimSpace
// variants into every test.
func trim(s string) string {
	return strings.TrimSpace(s)
}

// truncateAuditDetails returns s capped at auditDetailsMax bytes with a
// visible "…" marker. We cut at auditDetailsMax - suffix so the output
// stays at or below the cap regardless of input. Short inputs pass
// through unchanged.
func truncateAuditDetails(s string) string {
	if len(s) <= auditDetailsMax {
		return s
	}
	const suffix = "… (truncated)"
	cut := auditDetailsMax - len(suffix)
	if cut < 0 {
		cut = 0
	}
	return s[:cut] + suffix
}
