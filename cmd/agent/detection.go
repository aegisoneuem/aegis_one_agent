package main

import (
	"log/slog"

	"github.com/aegisoneuem/aegis_one_agent/internal/buffer"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

const (
	kindPatchScanResult = "patch_scan_result" // Layer 4 (weekly): missing updates from the cab scan
	kindDeviceState     = "device_state"      // Layer 1 (frequent): installed-KBs + build
)

// signalDrain wakes the sender to flush the buffer promptly (non-blocking +
// coalescing) so a freshly-buffered report does not wait for the next heartbeat.
func signalDrain(sig chan<- struct{}) {
	if sig == nil {
		return
	}
	select {
	case sig <- struct{}{}:
	default: // a wake is already pending; one is enough
	}
}

// drainReports sends buffered detection reports (Layer 1 device-state and Layer 4
// scan results) over the stream. Only the sender goroutine calls this, so there
// is no concurrent Send (detection goroutines only write to the buffer). A send
// failure keeps the item buffered for the next attempt.
func drainReports(stream pb.AgentControlPlane_ConnectClient, buf buffer.Buffer) {
	_, err := buf.Drain(func(kind string, payload []byte) error {
		switch kind {
		case kindDeviceState:
			var rep pb.DeviceStateReport
			if err := proto.Unmarshal(payload, &rep); err != nil {
				slog.Warn("drain: skipping corrupt device-state report", "error", err.Error())
				return nil
			}
			msg := &pb.AgentMessage{Payload: &pb.AgentMessage_DeviceState{DeviceState: &rep}}
			if err := stream.Send(msg); err != nil {
				return err // keep for retry
			}
			slog.Info("sent device-state report to server", "build", rep.Build, "installed_kbs", len(rep.InstalledKbs))
			return nil

		case kindPatchScanResult:
			var result pb.PatchScanResult
			if err := proto.Unmarshal(payload, &result); err != nil {
				slog.Warn("drain: skipping corrupt patch scan result", "error", err.Error())
				return nil
			}
			msg := &pb.AgentMessage{Payload: &pb.AgentMessage_PatchScanResult{PatchScanResult: &result}}
			if err := stream.Send(msg); err != nil {
				return err
			}
			slog.Info("sent reconciliation scan result to server", "build", result.Build, "missing", len(result.Missing))
			return nil

		default:
			return nil // unknown kind — drop
		}
	})
	if err != nil {
		slog.Warn("drain: sending buffered reports failed, will retry on next tick", "error", err.Error())
	}
}
