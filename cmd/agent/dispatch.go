package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	"github.com/aegisoneuem/aegis_one_agent/internal/deployspec"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

// taskHandler handles one dispatched (signature-verified) server command. Every
// task type registers a handler, so adding a new type (e.g. software_deploy) is a
// one-line registration — the receive loop never grows a hardcoded switch. This is
// the extensible-dispatch rule carried forward from the Phase-2 design.
type taskHandler func(ctx context.Context, client pb.AgentControlPlaneClient, cmd *pb.Command)

// dispatcher routes verified commands to per-type handlers. It holds the stable
// dependencies the handlers need (created once in main); the per-connection client
// + ctx are passed in on each dispatch.
type dispatcher struct {
	cfg                config.Config
	outbox             chan *pb.AgentMessage
	installJobs        chan installJob
	softdist           *softdistInstaller
	stg                *stager
	scanTrigger        chan<- struct{}
	deviceStateTrigger chan<- struct{}
	handlers           map[string]taskHandler
}

func newDispatcher(cfg config.Config, outbox chan *pb.AgentMessage, installJobs chan installJob, softdist *softdistInstaller, stg *stager, scanTrigger chan<- struct{}, deviceStateTrigger chan<- struct{}) *dispatcher {
	d := &dispatcher{cfg: cfg, outbox: outbox, installJobs: installJobs, softdist: softdist, stg: stg, scanTrigger: scanTrigger, deviceStateTrigger: deviceStateTrigger}
	d.handlers = map[string]taskHandler{
		"software_deploy_detect":   d.handleSoftwareDeployCmd,
		"install_patch":            d.handleInstallPatchCmd,
		"uninstall_patch":          d.handleUninstallPatchCmd,
		"stage_patch":              d.handleStagePatchCmd,
		"reboot":                   d.handleRebootCmd,
		"cancel_reboot":            d.handleCancelRebootCmd,
		"scan_now":                 d.handleScanNowCmd,
		"scan":                     d.handleScanNowCmd,
		"software_deploy":          d.handleSoftwareDeployCmd,
		"software_deploy_v2":       d.handleSoftwareDeployCmd,
		"software_deploy_bundle":   d.handleSoftwareDeployCmd,
		"software_deploy_workflow": d.handleSoftwareDeployCmd,
		"collect_file":             d.handleCollectFileCmd,
		"open_shell":               d.handleOpenShellCmd,
		"run_command":              d.handleRunCommandCmd,
		"file_operation":           d.handleFileOperationCmd,
		"report_device_state":      d.handleReportDeviceStateCmd,
		"audit_query":              d.handleAuditQueryCmd,
		"set_policy":               d.handleSetPolicyCmd,
		"remote_control":           d.handleRemoteControlCmd,
	}
	return d
}

// dispatch routes one verified command. Unknown types fall through to the generic
// command path (executeCommand) rather than failing — preserving prior behavior.
func (d *dispatcher) dispatch(ctx context.Context, client pb.AgentControlPlaneClient, cmd *pb.Command) {
	// Policy module gate: a signed command whose type is disabled by the active
	// agent policy is REFUSED before any handler runs — the command was
	// signature-valid, but the operator's own policy forbids it (high-security
	// profile / Ivanti-style module toggle). The refusal is a terminal status, so
	// it is audited as a command_completed with the blocking reason.
	ctype := strings.ToLower(strings.TrimSpace(cmd.Type))
	if !policyAllowsCommand(ctype) {
		reason := "blocked by agent policy: command type '" + ctype + "' is disabled on this endpoint"
		slog.Warn("policy: command blocked", "type", ctype, "command_id", cmd.CommandId)
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, ctype, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, reason))
		return
	}
	// Maintenance window (A3): a state-changing command (install/reboot/softdist)
	// that arrives OUTSIDE the active policy's window is DEFERRED — persisted and
	// run when the window opens. Read-only/control commands are never deferred.
	if gatedByMaintenance[ctype] && !policyInMaintenanceWindow(time.Now()) {
		d.deferCommand(ctx, cmd, ctype)
		return
	}
	d.routeCommand(ctx, client, cmd, ctype)
}

// routeCommand sends a command to its per-type handler (or the generic path). It
// is the post-gate routing step, shared by dispatch() and the deferred-queue drain.
func (d *dispatcher) routeCommand(ctx context.Context, client pb.AgentControlPlaneClient, cmd *pb.Command, ctype string) {
	if h := d.handlers[ctype]; h != nil {
		h(ctx, client, cmd)
		return
	}
	d.handleGenericCmd(ctx, client, cmd)
}

// deferCommand persists a command to the maintenance-window queue and reports it
// QUEUED. If persistence fails it reports FAILED rather than running outside the
// window (fail-closed — a maintenance window is a customer guarantee).
func (d *dispatcher) deferCommand(ctx context.Context, cmd *pb.Command, ctype string) {
	if err := persistDeferredCommand(d.cfg.SecureDir, cmd); err != nil {
		slog.Error("maintenance: could not persist deferred command", "command_id", cmd.CommandId, "type", ctype, "error", err.Error())
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, ctype, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, "deferred to maintenance window but persistence failed: "+err.Error()))
		return
	}
	slog.Info("maintenance: command deferred to window", "command_id", cmd.CommandId, "type", ctype)
	sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, ctype, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_QUEUED, "deferred to maintenance window"))
	recordAuditEvent(ctx, d.outbox, auditlog.Event{
		DeviceID: d.cfg.DeviceID, EventType: "command_deferred", Decision: "deferred",
		CommandID: cmd.CommandId, CommandType: ctype, SignatureOK: true,
		Reason: "outside maintenance window — will run when the window opens",
	})
}

// verifyDeferredCommand re-checks a deferred command's signature before it runs
// (defense in depth). A function var so tests can substitute it.
var verifyDeferredCommand = verifyCommand

// drainDeferred runs any queued commands once the maintenance window is open. It
// re-verifies each command's signature (defense in depth) and re-checks the
// policy gate (both may have changed while it was deferred). Called on connect
// and periodically; a no-op outside the window.
func (d *dispatcher) drainDeferred(ctx context.Context, client pb.AgentControlPlaneClient) {
	if !policyInMaintenanceWindow(time.Now()) {
		return
	}
	for _, cmd := range loadDeferredCommands(d.cfg.SecureDir) {
		if ctx.Err() != nil {
			return
		}
		ctype := strings.ToLower(strings.TrimSpace(cmd.Type))
		// Remove BEFORE running (at-most-once): a crashing command must not loop,
		// and installs have their own journal for crash recovery.
		removeDeferredCommand(d.cfg.SecureDir, cmd.CommandId)
		if !verifyDeferredCommand(cmd) {
			slog.Warn("maintenance: deferred command failed re-verification — dropping", "command_id", cmd.CommandId)
			continue
		}
		if !policyAllowsCommand(ctype) {
			slog.Info("maintenance: deferred command now blocked by policy — dropping", "command_id", cmd.CommandId, "type", ctype)
			continue
		}
		slog.Info("maintenance: window open — running deferred command", "command_id", cmd.CommandId, "type", ctype)
		d.routeCommand(ctx, client, cmd, ctype)
	}
}

// install_patch runs on the serial patch worker and reports phased ActionStatus;
// it must not block the receiver (DISM/wusa take minutes).
func (d *dispatcher) handleInstallPatchCmd(ctx context.Context, _ pb.AgentControlPlaneClient, cmd *pb.Command) {
	var spec pb.InstallPatchSpec
	if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
		slog.Error("install_patch: invalid payload", "command_id", cmd.CommandId, "error", err.Error())
		send(ctx, d.outbox, buildCommandResultMessage(cmd.CommandId, "failed", "invalid install payload"))
		return
	}
	slog.Info("install_patch: accepted, queued", "command_id", cmd.CommandId, "kb", spec.Kb)
	send(ctx, d.outbox, actionStatusMsg(&pb.ActionStatus{
		ActionId: cmd.CommandId, ActionType: actionPatchInstall, DeviceId: d.cfg.DeviceID,
		Kb: spec.Kb, Phase: pb.ActionPhase_ACTION_PHASE_QUEUED}))
	select {
	case d.installJobs <- installJob{commandID: cmd.CommandId, spec: &spec}:
	case <-ctx.Done():
	}
}

// uninstall_patch: remove an installed KB on the serial worker.
func (d *dispatcher) handleUninstallPatchCmd(ctx context.Context, _ pb.AgentControlPlaneClient, cmd *pb.Command) {
	var spec pb.InstallPatchSpec
	if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
		slog.Error("uninstall_patch: invalid payload", "command_id", cmd.CommandId, "error", err.Error())
		send(ctx, d.outbox, buildCommandResultMessage(cmd.CommandId, "failed", "invalid uninstall payload"))
		return
	}
	slog.Info("uninstall_patch: accepted, queued", "command_id", cmd.CommandId, "kb", spec.Kb)
	send(ctx, d.outbox, actionStatusMsg(&pb.ActionStatus{
		ActionId: cmd.CommandId, ActionType: actionPatchUninstall, DeviceId: d.cfg.DeviceID,
		Kb: spec.Kb, Phase: pb.ActionPhase_ACTION_PHASE_QUEUED}))
	select {
	case d.installJobs <- installJob{commandID: cmd.CommandId, spec: &spec, uninstall: true}:
	case <-ctx.Done():
	}
}

// software_deploy runs on the independent softdist worker (thin handler: validate
// + hand to delivery, never execute inline — the receiver must not block on a
// fetch or a minutes-long installer). Install/uninstall is one type, chosen by
// spec.action. Delivery streams the installer from the server when needed (or uses
// a pre-staged local file); the OS install serializes against patches via
// machineInstallGate inside the worker.
func (d *dispatcher) handleSoftwareDeployCmd(ctx context.Context, client pb.AgentControlPlaneClient, cmd *pb.Command) {
	detectionCommand := strings.EqualFold(strings.TrimSpace(cmd.Type), "software_deploy_detect")
	workflowCommand := strings.EqualFold(strings.TrimSpace(cmd.Type), "software_deploy_workflow")
	bundleCommand := workflowCommand || strings.EqualFold(strings.TrimSpace(cmd.Type), "software_deploy_bundle")
	v2 := detectionCommand || bundleCommand || strings.EqualFold(strings.TrimSpace(cmd.Type), "software_deploy_v2")
	if v2 {
		defer clear(cmd.Payload)
	}
	var spec pb.SoftwareDeploySpec
	if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
		slog.Error("software_deploy: invalid payload", "command_id", cmd.CommandId, "error", err.Error())
		send(ctx, d.outbox, buildCommandResultMessage(cmd.CommandId, "failed", "invalid software_deploy payload"))
		return
	}
	if spec.Detection != nil || detectionCommand {
		var detectionErr error
		if !detectionCommand {
			detectionErr = fmt.Errorf("advanced detection requires signed software_deploy_detect; no fallback")
		} else {
			detectionErr = deployspec.ValidateDetection(&spec)
		}
		if detectionErr != nil {
			clear(cmd.Payload)
			clearPrivateDeployment(&spec)
			send(ctx, d.outbox, actionStatusMsg(simpleActionStatus(cmd.CommandId, actionSoftwareDeploy, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, detectionErr.Error())))
			return
		}
		workflowCommand = spec.Steps != nil
		bundleCommand = spec.Bundle != nil
	}
	if spec.Steps != nil || workflowCommand {
		var validationErr error
		if !workflowCommand {
			validationErr = fmt.Errorf("deployment steps require signed software_deploy_workflow; no fallback")
		} else {
			validationErr = validateWorkflowDeployment(&spec)
		}
		if validationErr != nil {
			clear(cmd.Payload)
			clearPrivateDeployment(&spec)
			send(ctx, d.outbox, actionStatusMsg(simpleActionStatus(cmd.CommandId, actionSoftwareDeploy, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, validationErr.Error())))
			return
		}
	}
	if (spec.Bundle != nil && !bundleCommand) || (bundleCommand && spec.Bundle == nil) {
		clear(cmd.Payload)
		clearPrivateDeployment(&spec)
		send(ctx, d.outbox, actionStatusMsg(simpleActionStatus(cmd.CommandId, actionSoftwareDeploy, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, "bundle settings require signed software_deploy_bundle; no fallback")))
		return
	}
	if !v2 && privateDeployment(&spec) {
		clear(cmd.Payload)
		clearPrivateDeployment(&spec)
		send(ctx, d.outbox, actionStatusMsg(simpleActionStatus(cmd.CommandId, actionSoftwareDeploy, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, "execution contexts and secure parameters require signed software_deploy_v2; no SYSTEM fallback")))
		return
	}
	if v2 {
		if spec.Execution == nil {
			spec.Execution = &pb.SoftwareExecution{Mode: "system"}
		}
		if err := validatePrivateDeployment(&spec, d.cfg.SoftdistMaxSecretBytes); err != nil {
			clear(spec.Execution.PasswordUtf8)
			clear(spec.SecureParameters)
			send(ctx, d.outbox, actionStatusMsg(simpleActionStatus(cmd.CommandId, actionSoftwareDeploy, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, err.Error())))
			return
		}
	}
	if bundleCommand {
		if err := validateBundleDeployment(&spec); err != nil {
			clearPrivateDeployment(&spec)
			send(ctx, d.outbox, actionStatusMsg(simpleActionStatus(cmd.CommandId, actionSoftwareDeploy, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, err.Error())))
			return
		}
	}
	slog.Info("software_deploy: accepted", "command_id", cmd.CommandId,
		"name", spec.Name, "version", spec.Version, "action", spec.Action)
	send(ctx, d.outbox, actionStatusMsg(simpleActionStatus(cmd.CommandId, actionSoftwareDeploy, d.cfg.DeviceID, "",
		pb.ActionPhase_ACTION_PHASE_QUEUED, "queued")))
	d.softdist.deliver(ctx, client, cmd.CommandId, &spec)
}

// stage_patch: pre-cache a patch (encrypted, StateStaged) ahead of a scheduled
// install. Fetched via peer-cache in the background so it does not block the receiver.
func (d *dispatcher) handleStagePatchCmd(ctx context.Context, client pb.AgentControlPlaneClient, cmd *pb.Command) {
	var spec pb.InstallPatchSpec
	if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
		slog.Error("stage_patch: invalid payload", "command_id", cmd.CommandId, "error", err.Error())
		send(ctx, d.outbox, buildCommandResultMessage(cmd.CommandId, "failed", "invalid stage payload"))
		return
	}
	slog.Info("stage_patch: accepted", "command_id", cmd.CommandId, "kb", spec.Kb)
	d.stg.stage(ctx, client, cmd.CommandId, &spec)
}

// reboot: server-driven (signed) system reboot.
func (d *dispatcher) handleRebootCmd(ctx context.Context, _ pb.AgentControlPlaneClient, cmd *pb.Command) {
	// Policy reboot_policy=never forbids reboots on this endpoint (maintenance /
	// always-on servers). "always"/"if_required" shape auto-reboot in the install
	// path; blocking an explicit reboot here is the clean, concrete enforcement.
	if getActivePolicy().RebootPolicy() == "never" {
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionReboot, d.cfg.DeviceID, "",
			pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE, "reboot blocked by agent policy (reboot_policy=never)"))
		return
	}
	var spec pb.RebootSpec
	if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
		send(ctx, d.outbox, buildCommandResultMessage(cmd.CommandId, "failed", "invalid reboot payload"))
		return
	}
	status, message := handleReboot(&spec)
	send(ctx, d.outbox, actionStatusMsg(simpleActionStatus(cmd.CommandId, actionReboot, d.cfg.DeviceID, "", rebootPhase(status), message)))
}

// cancel_reboot: cancel a pending server-scheduled reboot.
func (d *dispatcher) handleCancelRebootCmd(ctx context.Context, _ pb.AgentControlPlaneClient, cmd *pb.Command) {
	status, message := handleCancelReboot()
	send(ctx, d.outbox, actionStatusMsg(simpleActionStatus(cmd.CommandId, actionReboot, d.cfg.DeviceID, "", rebootPhase(status), message)))
}

// scan_now / scan: on-demand compliance scan. Fire the same trigger a new cab uses;
// the scan runs in the detection goroutine and streams its own scan ActionStatus.
func (d *dispatcher) handleScanNowCmd(ctx context.Context, _ pb.AgentControlPlaneClient, cmd *pb.Command) {
	slog.Info("scan_now: on-demand compliance scan requested", "command_id", cmd.CommandId)
	signalDrain(d.scanTrigger)
	send(ctx, d.outbox, buildCommandResultMessage(cmd.CommandId, "accepted", "compliance scan triggered"))
}

// report_device_state: on-demand DeviceStateReport refresh. The Layer-1 fast
// lane normally sends only on change (fingerprint diff) so the server has no
// cached state for devices that have not changed recently. This command resets
// the agent's fingerprint cache and triggers an immediate run — the server
// sees a fresh DeviceStateReport within one cycle regardless of change state.
// Used by the console / NSH "inventory refresh" to pull a snapshot on demand.
func (d *dispatcher) handleReportDeviceStateCmd(ctx context.Context, _ pb.AgentControlPlaneClient, cmd *pb.Command) {
	slog.Info("report_device_state: on-demand inventory refresh requested", "command_id", cmd.CommandId)
	signalDrain(d.deviceStateTrigger)
	send(ctx, d.outbox, buildCommandResultMessage(cmd.CommandId, "accepted", "device state refresh triggered"))
}

// handleGenericCmd runs any non-registered command type through executeCommand and
// acks the result (hostname, inventory probes, etc.).
func (d *dispatcher) handleGenericCmd(ctx context.Context, _ pb.AgentControlPlaneClient, cmd *pb.Command) {
	output, execErr := executeCommand(d.cfg, cmd.Type, string(cmd.Payload))
	status, message := "success", "command received and acknowledged"
	if execErr != nil {
		status, message = "failed", execErr.Error()
	} else if output != "" {
		message = strings.TrimSpace(output)
	}
	slog.Info("command executed", "command_id", cmd.CommandId, "type", cmd.Type, "status", status)
	send(ctx, d.outbox, buildCommandResultMessage(cmd.CommandId, status, message))
}
