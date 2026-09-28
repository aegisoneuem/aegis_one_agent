package main

import (
	"log/slog"

	"github.com/aegisoneuem/aegis_one_agent/internal/buffer"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

const kindPatchScanResult = "patch_scan_result"

// drainPatchResults sends any buffered patch scan results over the stream. Only
// the main connect loop calls this, so there is no concurrent Send on the stream
// (the detection goroutine only ever writes to the buffer). A send failure keeps
// the item buffered for the next attempt.
func drainPatchResults(stream pb.AgentControlPlane_ConnectClient, buf buffer.Buffer) {
	_, err := buf.Drain(func(kind string, payload []byte) error {
		if kind != kindPatchScanResult {
			return nil
		}
		var result pb.PatchScanResult
		if err := proto.Unmarshal(payload, &result); err != nil {
			slog.Warn("drain: skipping corrupt patch scan result", "error", err.Error())
			return nil // drop corrupt record
		}
		msg := &pb.AgentMessage{Payload: &pb.AgentMessage_PatchScanResult{PatchScanResult: &result}}
		if err := stream.Send(msg); err != nil {
			return err // keep for retry
		}
		slog.Info("sent patch scan result to server", "build", result.Build, "missing", len(result.Missing))
		return nil
	})
	if err != nil {
		slog.Warn("drain: sending buffered patch results failed, will retry on next tick", "error", err.Error())
	}
}
