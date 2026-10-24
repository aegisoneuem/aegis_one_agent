package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/fileops"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// Signed JSON file_operation/v1 reuses FetchContent/UploadContent. All filesystem
// operations are native Go; no SMB credentials or PowerShell dependency.
func (d *dispatcher) handleFileOperationCmd(ctx context.Context, client pb.AgentControlPlaneClient, cmd *pb.Command) {
	var spec fileops.Spec
	if err := json.Unmarshal(cmd.Payload, &spec); err != nil || spec.Version != 1 || len(cmd.Payload) > fileops.MaxManifestBytes {
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, "file_operation", d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, "invalid file_operation/v1 payload"))
		return
	}
	go func() {
		defer recoverLog("file-operation")
		slog.Info("file_operation accepted", "command_id", cmd.CommandId, "operation", spec.Operation, "source", spec.Source, "destination", spec.Destination, "recursive", spec.Recursive, "overwrite", spec.Overwrite)
		started := time.Now()
		timeout := time.Duration(spec.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = rcDefaultTimeout * time.Second
		}
		workCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, "file_operation", d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_RUNNING, "file operation running"))
		result, err := d.runFileOperation(workCtx, client, cmd.CommandId, spec)
		phase := pb.ActionPhase_ACTION_PHASE_SUCCESS
		reason := fmt.Sprintf("%s: %d file(s), %d bytes", spec.Operation, result.Files, result.Bytes)
		if spec.Source != "" {
			reason += "; " + spec.Source
		}
		if spec.Destination != "" {
			reason += " -> " + spec.Destination
		}
		if err != nil {
			phase = pb.ActionPhase_ACTION_PHASE_FAILED
			reason = err.Error()
		}
		exitCode := int32(0)
		if err != nil {
			exitCode = 1
		}
		detail, _ := json.Marshal(result)
		if len(detail) > fileops.MaxManifestBytes {
			result.Entries = nil
			detail, _ = json.Marshal(result)
			phase = pb.ActionPhase_ACTION_PHASE_FAILED
			exitCode = 1
			reason = "file manifest exceeds 2 MiB; use smaller batches"
		}
		sendStatus(d.outbox, &pb.ActionStatus{ActionId: cmd.CommandId, ActionType: "file_operation", DeviceId: d.cfg.DeviceID, Phase: phase, ExitCode: exitCode, Reason: reason, Detail: string(detail), StartedAt: started.Unix(), FinishedAt: time.Now().Unix()})
		slog.Info("file_operation finished", "command_id", cmd.CommandId, "operation", spec.Operation, "phase", phase.String(), "reason", reason)
	}()
}

func (d *dispatcher) runFileOperation(ctx context.Context, client pb.AgentControlPlaneClient, id string, spec fileops.Spec) (fileops.Result, error) {
	if spec.WorkingDir == "" {
		spec.WorkingDir = filepath.Join(d.cfg.SecureDir, "tmp")
	}
	if spec.Operation != "pack" && spec.Operation != "unpack" && spec.Operation != "copy" && spec.Operation != "move" {
		return fileops.Basic(ctx, spec)
	}
	tmpRoot := filepath.Join(d.cfg.SecureDir, "tmp")
	if err := os.MkdirAll(tmpRoot, 0700); err != nil {
		return fileops.Result{}, err
	}
	f, err := os.CreateTemp(tmpRoot, "file-transfer-*.zip")
	if err != nil {
		return fileops.Result{}, err
	}
	archive := f.Name()
	f.Close()
	os.Remove(archive)
	defer os.Remove(archive)
	if spec.Operation == "copy" || spec.Operation == "move" {
		packed, err := fileops.Pack(ctx, spec, archive)
		if err != nil {
			return packed, err
		}
		spec.Entries = packed.Entries
		if err = fileops.ValidateLocalCopy(spec); err != nil {
			return packed, err
		}
		result, err := fileops.Unpack(ctx, spec, archive)
		if err != nil {
			return result, err
		}
		if spec.Operation == "move" {
			_, err = fileops.DeleteManifest(ctx, packed.Entries)
		}
		return result, err
	}
	if spec.Operation == "pack" {
		result, err := fileops.Pack(ctx, spec, archive)
		if err != nil {
			return result, err
		}
		manifest, _ := json.Marshal(result)
		if len(manifest) > fileops.MaxManifestBytes {
			result.Entries = nil
			return result, fmt.Errorf("file manifest exceeds 2 MiB; copy in smaller batches")
		}
		_, result.StoredPath, err = uploadFile(ctx, client, id, "bundle.zip", archive, 0, 0)
		return result, err
	}
	if spec.ContentID == "" || len(spec.SHA256) != 64 {
		return fileops.Result{}, fmt.Errorf("file transfer requires content_id and SHA-256")
	}
	if err = fetchContent(ctx, client, &pb.ContentRequest{Kind: "nsh-file", Id: spec.ContentID, Sha256: spec.SHA256}, archive); err != nil {
		return fileops.Result{}, err
	}
	return fileops.Unpack(ctx, spec, archive)
}
