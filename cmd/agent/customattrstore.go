package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

// Custom-attribute definition store. The server pushes a SIGNED CustomAttributeSpec
// (as a set_custom_attributes command); the agent persists the signed envelope,
// applies anti-rollback on issued_at, re-verifies it at startup, and evaluates the
// definitions into FullInventoryReport.custom_properties each inventory cycle.

var (
	customAttrMu       sync.RWMutex
	customAttrDefs     []attrDefinition
	customAttrIssuedAt int64
)

// storedCustomAttr is the signed envelope persisted to disk (so definitions
// survive restarts and can be re-verified). Payload is the marshaled
// CustomAttributeSpec proto bytes.
type storedCustomAttr struct {
	Type      string `json:"type"`
	CommandID string `json:"command_id"`
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

func customAttrStorePath(secureDir string) string {
	return filepath.Join(secureDir, "custom-attr.json")
}

// specToDefs converts the proto spec into evaluator definitions.
func specToDefs(spec *pb.CustomAttributeSpec) []attrDefinition {
	out := make([]attrDefinition, 0, len(spec.GetDefinitions()))
	for _, d := range spec.GetDefinitions() {
		out = append(out, attrDefinition{
			Name:     d.GetName(),
			Provider: d.GetProvider(),
			Args:     d.GetArgs(),
		})
	}
	return out
}

// loadStoredCustomAttr reads and RE-VERIFIES the persisted definitions at startup.
// An invalid signature (tampered file, or wrong key) means the definitions are
// dropped — the agent reports no custom properties rather than trusting edited
// definitions. Missing file is a clean no-op.
func loadStoredCustomAttr(secureDir string) {
	data, err := os.ReadFile(customAttrStorePath(secureDir))
	if err != nil {
		return // none yet
	}
	var sc storedCustomAttr
	if err := json.Unmarshal(data, &sc); err != nil {
		slog.Warn("custom-attr: stored file is not valid JSON — ignoring")
		return
	}
	if !verifyCommand(&pb.Command{Type: sc.Type, CommandId: sc.CommandID, Payload: sc.Payload, Signature: sc.Signature}) {
		slog.Warn("custom-attr: stored definitions FAILED signature re-verification — ignoring (tampered or wrong key)")
		return
	}
	var spec pb.CustomAttributeSpec
	if err := proto.Unmarshal(sc.Payload, &spec); err != nil {
		slog.Warn("custom-attr: stored payload not a valid CustomAttributeSpec — ignoring")
		return
	}
	customAttrMu.Lock()
	customAttrDefs = specToDefs(&spec)
	customAttrIssuedAt = spec.GetIssuedAt()
	customAttrMu.Unlock()
	slog.Info("custom-attr: loaded persisted definitions", "count", len(spec.GetDefinitions()), "issued_at", spec.GetIssuedAt())
}

// currentCustomProperties evaluates the active definitions into a name->value map
// for the inventory report. Returns nil when no definitions are active.
func currentCustomProperties() map[string]string {
	customAttrMu.RLock()
	defs := customAttrDefs
	customAttrMu.RUnlock()
	if len(defs) == 0 {
		return nil
	}
	m := evalDefinitions(defs)
	if len(m) == 0 {
		return nil
	}
	return m
}

// handleSetCustomAttrCmd applies a signed set_custom_attributes command: parse the
// spec, reject a non-newer one (anti-rollback on issued_at), persist the signed
// envelope, make the definitions active, and audit. The command was already
// Ed25519-verified by the receive loop before dispatch.
func (d *dispatcher) handleSetCustomAttrCmd(ctx context.Context, _ pb.AgentControlPlaneClient, cmd *pb.Command) {
	var spec pb.CustomAttributeSpec
	if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionSetCustomAttr, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, "invalid custom-attribute payload"))
		return
	}
	customAttrMu.RLock()
	activeIssued := customAttrIssuedAt
	customAttrMu.RUnlock()
	if spec.GetIssuedAt() <= activeIssued {
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionSetCustomAttr, d.cfg.DeviceID, "",
			pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE, "custom-attribute spec not newer than the active one (anti-rollback)"))
		return
	}
	sc := storedCustomAttr{Type: cmd.Type, CommandID: cmd.CommandId, Payload: append([]byte(nil), cmd.Payload...), Signature: cmd.Signature}
	if err := writeCustomAttrStore(d.cfg.SecureDir, sc); err != nil {
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionSetCustomAttr, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, "persist custom-attributes failed: "+err.Error()))
		return
	}
	defs := specToDefs(&spec)
	customAttrMu.Lock()
	customAttrDefs = defs
	customAttrIssuedAt = spec.GetIssuedAt()
	customAttrMu.Unlock()

	recordAuditEvent(ctx, d.outbox, auditlog.Event{
		DeviceID:  d.cfg.DeviceID,
		EventType: "custom_attributes_applied",
		CommandID: cmd.CommandId,
		Actor:     cmd.Actor,
		Source:    cmd.Source,
		Decision:  "applied",
		Reason:    "signed custom-attribute definitions applied",
	})
	slog.Info("custom-attr: applied definitions", "command_id", cmd.CommandId, "count", len(defs), "issued_at", spec.GetIssuedAt())
	sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionSetCustomAttr, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_SUCCESS, "custom-attribute definitions applied"))
}

// writeCustomAttrStore persists the signed envelope atomically (temp + rename), 0600.
func writeCustomAttrStore(secureDir string, sc storedCustomAttr) error {
	raw, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(secureDir, 0o700); err != nil {
		return err
	}
	path := customAttrStorePath(secureDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
