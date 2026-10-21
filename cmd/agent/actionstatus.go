package main

import (
	"time"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// Action type labels for ActionStatus.action_type — the console groups and
// filters per-action status by these, the same way BigFix/ManageEngine/Tanium do.
const (
	actionPatchInstall   = "patch_install"
	actionPatchUninstall = "patch_uninstall"
	actionStage          = "stage"
	actionScan           = "scan"
	actionReboot         = "reboot"
	actionSoftwareDeploy = "software_deploy"
	actionCollectFile    = "collect_file"
	actionOpenShell      = "open_shell"
	actionRunCommand     = "run_command"
)

// actionStatusMsg wraps an ActionStatus in an AgentMessage for the outbox/stream.
func actionStatusMsg(st *pb.ActionStatus) *pb.AgentMessage {
	return &pb.AgentMessage{Payload: &pb.AgentMessage_ActionStatus{ActionStatus: st}}
}

// actionPhaseTerminal reports whether a phase is an end state (no more updates).
func actionPhaseTerminal(p pb.ActionPhase) bool {
	switch p {
	case pb.ActionPhase_ACTION_PHASE_SUCCESS,
		pb.ActionPhase_ACTION_PHASE_FAILED,
		pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE:
		return true
	default:
		return false
	}
}

// rebootPhase maps a reboot handler's status string ("success"/"failed") to the
// unified action phase.
func rebootPhase(status string) pb.ActionPhase {
	if status == "success" {
		return pb.ActionPhase_ACTION_PHASE_SUCCESS
	}
	return pb.ActionPhase_ACTION_PHASE_FAILED
}

// simpleActionStatus builds a one-shot ActionStatus (no exit code / detail) for
// the lighter actions (stage, reboot, scan). finished_at is stamped on terminal
// phases. patch installs build their status directly to carry exit_code/detail.
func simpleActionStatus(actionID, actionType, deviceID, kb string, phase pb.ActionPhase, reason string) *pb.ActionStatus {
	now := time.Now().Unix()
	st := &pb.ActionStatus{
		ActionId:   actionID,
		ActionType: actionType,
		DeviceId:   deviceID,
		Kb:         kb,
		Phase:      phase,
		Reason:     reason,
		StartedAt:  now,
	}
	if actionPhaseTerminal(phase) {
		st.FinishedAt = now
	}
	return st
}
