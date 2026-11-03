// Package capabilities exposes the stable list of task types and feature
// flags the agent supports on this build. The server reads this list on
// Heartbeat / Enroll and dispatches only known-supported action types;
// older agents are safely insulated from newer task kinds this way.
//
// The list is intentionally hard-coded (not reflected from the dispatcher
// registry) so adding a new task type requires a deliberate edit here —
// cheap to review, impossible to accidentally advertise an unimplemented
// capability. A unit test in cmd/agent cross-checks that every dispatch
// handler has a matching capabilities entry.
package capabilities

// schemaVersion is the stable version of the telemetry contract this agent
// speaks. The server gates feature rollouts on this value; bump it only on
// a BREAKING proto / semantics change (additive fields do not bump it).
const schemaVersion = "1"

// supported lists every task type and feature flag this agent build
// recognizes. Ordering is stable — tests pin it so an accidental reorder
// is caught in review. Append new entries at the end rather than in the
// middle, so a change is visible at a glance.
var supported = []string{
	// SoftDist family
	"software_deploy",
	"software_deploy_v2",
	"software_deploy_bundle",
	"software_deploy_workflow",
	"software_deploy_detect",
	// Patch family
	"install_patch",
	"uninstall_patch",
	"stage_patch",
	"scan_now",
	"scan",
	// Device lifecycle
	"reboot",
	"cancel_reboot",
	"report_device_state",
	// NSH / operator
	"run_command",
	"open_shell",
	"collect_file",
	"file_operation",
	// Feature flags (not task types; "the agent has this capability")
	"script_content_audit",
	"hw_inventory",
	"full_inventory_report",
	"inventory_refresh_on_demand",
	"bandwidth_throttle",
	"hku_per_user_inventory",
	// UC-21.1 — closes BigFix detailed-scan gaps
	"hw_detailed_scan",
	// Phase 1b — native remote file write (no PowerShell dependency)
	"file_operation_write_bytes",
}

// SchemaVersion returns the stable contract version the server uses to
// gate feature rollouts.
func SchemaVersion() string { return schemaVersion }

// List returns a defensive copy of the supported action-type /
// feature-flag list so callers (Heartbeat / Enroll construction) cannot
// mutate the package-level slice.
func List() []string {
	out := make([]string, len(supported))
	copy(out, supported)
	return out
}

// DeviceType returns the Phase-1 default role for an Aegis endpoint
// agent. Phase 2 will introduce "probe" (NMS collector) alongside this;
// the field exists in the proto now so adding "probe" later is non-
// breaking at the wire level.
func DeviceType() string { return "endpoint" }
