package main

import (
	"log/slog"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// installJob is a patch install handed from the receiver to the serial worker.
type installJob struct {
	commandID string
	spec      *pb.InstallPatchSpec
}

// patchWorker runs patch installs ONE AT A TIME. The Windows servicing stack
// (CBS/TrustedInstaller) processes a single operation at a time, so concurrent
// DISM installs would conflict — patches must be serialized. This worker is
// process-level and survives reconnects; it emits phased InstallStatus to the
// outbox, which the current connection's sender drains.
//
// Only patch installs go through this serial worker. Other task types (package
// deploy, config, DEX, inventory, commands) are independent of the servicing
// stack and are free to run concurrently — this design does not serialize them.
func patchWorker(jobs <-chan installJob, outbox chan<- *pb.AgentMessage) {
	for job := range jobs {
		slog.Info("install_patch: running", "command_id", job.commandID, "kb", job.spec.Kb)
		sendStatus(outbox, &pb.InstallStatus{
			CommandId: job.commandID,
			Kb:        job.spec.Kb,
			Phase:     pb.InstallPhase_INSTALL_PHASE_RUNNING,
		})

		st := handleInstallPatch(job.spec)
		st.CommandId = job.commandID
		sendStatus(outbox, st)

		slog.Info("install_patch: finished",
			"command_id", job.commandID, "phase", st.Phase.String(),
			"exit_code", st.ExitCode, "reboot_required", st.RebootRequired, "reason", st.Reason)
	}
}

// sendStatus queues an InstallStatus for the sender goroutine to transmit.
func sendStatus(outbox chan<- *pb.AgentMessage, st *pb.InstallStatus) {
	outbox <- &pb.AgentMessage{Payload: &pb.AgentMessage_InstallStatus{InstallStatus: st}}
}
