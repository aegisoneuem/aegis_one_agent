package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// softdistJob is a software deploy handed from the receiver to the softdist
// worker. Software deploys run on their OWN serial queue — independent of the
// patch queue so neither blocks the other's intake — but both acquire
// machineInstallGate around the actual OS install, so only one installer ever
// runs at a time (Windows serializes MSI; CBS + MSI collide — see install.go).
type softdistJob struct {
	commandID string
	spec      *pb.SoftwareDeploySpec
}

// softdistInstaller carries what the worker needs across deploys.
type softdistInstaller struct {
	outbox   chan<- *pb.AgentMessage
	deviceID string
}

func newSoftdistInstaller(outbox chan<- *pb.AgentMessage, deviceID string) *softdistInstaller {
	return &softdistInstaller{outbox: outbox, deviceID: deviceID}
}

// worker runs software deploys ONE AT A TIME on this queue. Each job is isolated
// with safeRun so one deploy's panic cannot kill the worker (which would stall
// every later deploy) — the agent stays up and moves to the next job.
func (s *softdistInstaller) worker(jobs <-chan softdistJob) {
	for job := range jobs {
		job := job
		safeRun("softdist-deploy", func() { s.run(job) })
	}
}

// run executes one deploy and streams its terminal ActionStatus. QUEUED is
// emitted by the receiver when the job is accepted; this emits RUNNING (it may
// block on the shared install lane) and then the terminal phase.
func (s *softdistInstaller) run(job softdistJob) {
	spec := job.spec
	cmdID := job.commandID
	started := time.Now().Unix()

	emitRunning := func(reason string) {
		sendStatus(s.outbox, &pb.ActionStatus{
			ActionId: cmdID, ActionType: actionSoftwareDeploy, DeviceId: s.deviceID,
			Phase: pb.ActionPhase_ACTION_PHASE_RUNNING, Reason: reason, StartedAt: started,
		})
	}

	slog.Info("software_deploy: starting", "command_id", cmdID,
		"name", spec.Name, "version", spec.Version, "action", spec.Action, "type", spec.InstallerType)
	emitRunning("starting install")

	// Acquire the single machine-install lane (shared with patches): only one
	// installer runs at a time. If a patch or another deploy holds it, we wait here.
	machineInstallGate.Lock()
	defer machineInstallGate.Unlock()

	res := softdist.Run(context.Background(), softdist.Params{
		Name:          spec.Name,
		Action:        spec.Action,
		InstallerType: spec.InstallerType,
		FilePath:      spec.FilePath,
		SilentArgs:    spec.SilentArgs,
		ExpectedHash:  spec.ExpectedHash,
		HashAlgorithm: spec.HashAlgorithm,
		SuccessCodes:  spec.SuccessExitCodes,
		Timeout:       time.Duration(spec.TimeoutSeconds) * time.Second,
	})

	var phase pb.ActionPhase
	switch {
	case res.NotApplicable:
		phase = pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE
	case res.Success:
		phase = pb.ActionPhase_ACTION_PHASE_SUCCESS
	default:
		phase = pb.ActionPhase_ACTION_PHASE_FAILED
	}
	sendStatus(s.outbox, &pb.ActionStatus{
		ActionId: cmdID, ActionType: actionSoftwareDeploy, DeviceId: s.deviceID,
		Phase: phase, ExitCode: int32(res.ExitCode), RebootRequired: res.RebootRequired,
		Reason: res.Reason, Detail: res.Detail, StartedAt: started, FinishedAt: time.Now().Unix(),
	})
	slog.Info("software_deploy: finished", "command_id", cmdID, "name", spec.Name,
		"phase", phase.String(), "exit_code", res.ExitCode, "reboot_required", res.RebootRequired, "reason", res.Reason)
}
