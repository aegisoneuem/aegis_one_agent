package main

import (
	"context"
	"log/slog"
	"strings"

	"github.com/aegisoneuem/aegis_one_agent/internal/config"
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
	cfg         config.Config
	outbox      chan *pb.AgentMessage
	installJobs chan installJob
	softdist    *softdistInstaller
	stg         *stager
	scanTrigger chan<- struct{}
	handlers    map[string]taskHandler
}

func newDispatcher(cfg config.Config, outbox chan *pb.AgentMessage, installJobs chan installJob, softdist *softdistInstaller, stg *stager, scanTrigger chan<- struct{}) *dispatcher {
	d := &dispatcher{cfg: cfg, outbox: outbox, installJobs: installJobs, softdist: softdist, stg: stg, scanTrigger: scanTrigger}
	d.handlers = map[string]taskHandler{
		"install_patch":      d.handleInstallPatchCmd,
		"uninstall_patch":    d.handleUninstallPatchCmd,
		"stage_patch":        d.handleStagePatchCmd,
		"reboot":             d.handleRebootCmd,
		"cancel_reboot":      d.handleCancelRebootCmd,
		"scan_now":           d.handleScanNowCmd,
		"scan":               d.handleScanNowCmd,
		"software_deploy":    d.handleSoftwareDeployCmd,
		"software_deploy_v2": d.handleSoftwareDeployCmd,
		"collect_file":       d.handleCollectFileCmd,
		"open_shell":         d.handleOpenShellCmd,
		"run_command":        d.handleRunCommandCmd,
		"file_operation":     d.handleFileOperationCmd,
	}
	return d
}

// dispatch routes one verified command. Unknown types fall through to the generic
// command path (executeCommand) rather than failing — preserving prior behavior.
func (d *dispatcher) dispatch(ctx context.Context, client pb.AgentControlPlaneClient, cmd *pb.Command) {
	if h := d.handlers[strings.ToLower(strings.TrimSpace(cmd.Type))]; h != nil {
		h(ctx, client, cmd)
		return
	}
	d.handleGenericCmd(ctx, client, cmd)
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
	v2 := strings.EqualFold(strings.TrimSpace(cmd.Type), "software_deploy_v2")
	if v2 {
		defer clear(cmd.Payload)
	}
	var spec pb.SoftwareDeploySpec
	if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
		slog.Error("software_deploy: invalid payload", "command_id", cmd.CommandId, "error", err.Error())
		send(ctx, d.outbox, buildCommandResultMessage(cmd.CommandId, "failed", "invalid software_deploy payload"))
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
