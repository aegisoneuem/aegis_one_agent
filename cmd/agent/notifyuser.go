package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	"github.com/aegisoneuem/aegis_one_agent/internal/uimsg"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

const actionNotifyUser = "notify_user"

// notifyUserPayload is the signed command payload for a user notification.
//   - kind "info" (default) → tray balloon, auto-dismiss, no response.
//   - kind "message"        → a window the user reads and closes, no response.
//   - kind "action"         → an action request with buttons/deadline/snooze; the
//     user's choice is reported back against this command.
type notifyUserPayload struct {
	Kind         string   `json:"kind"`
	Title        string   `json:"title"`
	Text         string   `json:"text"`
	Buttons      []string `json:"buttons"`
	DeadlineUnix int64    `json:"deadline_unix"`
	AllowSnooze  bool     `json:"allow_snooze"`
}

// pendingUIAction remembers an outstanding action request so the user's later response
// can be reported against the originating command.
type pendingUIAction struct {
	commandID string
	deviceID  string
	outbox    chan<- *pb.AgentMessage
	actor     string
	source    string
}

var (
	pendingMu      sync.Mutex
	pendingActions = map[string]*pendingUIAction{}
)

// showUserNotification is a convenience for an info balloon (used by patch-flow hooks).
func showUserNotification(title, text string) int {
	return deliverUIMessage(uimsg.Message{Kind: uimsg.KindNotify, Title: title, Text: text})
}

func (d *dispatcher) handleNotifyUserCmd(ctx context.Context, _ pb.AgentControlPlaneClient, cmd *pb.Command) {
	var p notifyUserPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		parts := strings.SplitN(strings.TrimSpace(string(cmd.Payload)), "|", 2)
		p.Title = strings.TrimSpace(parts[0])
		if len(parts) > 1 {
			p.Text = strings.TrimSpace(parts[1])
		}
	}
	p.Title = strings.Trim(strings.TrimSpace(p.Title), `"`)
	p.Text = strings.Trim(strings.TrimSpace(p.Text), `"`)
	if p.Title == "" {
		p.Title = "AegisOne Client"
	}

	kind := uimsg.KindNotify
	switch strings.ToLower(strings.TrimSpace(p.Kind)) {
	case "message":
		kind = uimsg.KindMessage
	case "action":
		kind = uimsg.KindAction
	}

	msg := uimsg.Message{
		Kind: kind, ID: cmd.CommandId, Title: p.Title, Text: p.Text,
		Buttons: p.Buttons, DeadlineUnix: p.DeadlineUnix, AllowSnooze: p.AllowSnooze,
	}

	// For an action we must wait for the user's choice before reporting final status;
	// register it so the response (same ID) maps back to this command.
	if kind == uimsg.KindAction {
		pendingMu.Lock()
		pendingActions[cmd.CommandId] = &pendingUIAction{
			commandID: cmd.CommandId, deviceID: d.cfg.DeviceID, outbox: d.outbox,
			actor: cmd.Actor, source: cmd.Source,
		}
		pendingMu.Unlock()
	}

	n := deliverUIMessage(msg)
	if n == 0 {
		if kind == uimsg.KindAction {
			pendingMu.Lock()
			delete(pendingActions, cmd.CommandId)
			pendingMu.Unlock()
		}
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionNotifyUser, d.cfg.DeviceID, "",
			pb.ActionPhase_ACTION_PHASE_FAILED, "no tray UI connected — notification not shown (user not logged on, or UI not running)"))
		return
	}

	if kind == uimsg.KindAction {
		// Shown — final status comes when the user responds (reportUserResponse).
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionNotifyUser, d.cfg.DeviceID, "",
			pb.ActionPhase_ACTION_PHASE_RUNNING, fmt.Sprintf("action shown to %d user session(s) — awaiting response", n)))
		return
	}
	sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionNotifyUser, d.cfg.DeviceID, "",
		pb.ActionPhase_ACTION_PHASE_SUCCESS, fmt.Sprintf("notification delivered to %d connected user session(s)", n)))
}

// reportUserResponse maps a UI response back to its originating action command and
// reports the user's choice to the server (audited). Called from the pipe reader.
func reportUserResponse(id, action string, snoozeSecs int64) {
	pendingMu.Lock()
	pa := pendingActions[id]
	delete(pendingActions, id)
	pendingMu.Unlock()
	if pa == nil {
		return // unknown / already-reported action
	}
	reason := "user chose: " + action
	if action == "snoozed" && snoozeSecs > 0 {
		reason = fmt.Sprintf("user snoozed for %ds", snoozeSecs)
	}
	recordAuditEvent(context.Background(), pa.outbox, auditlog.Event{
		DeviceID: pa.deviceID, EventType: "user_action_response", CommandID: pa.commandID,
		Actor: pa.actor, Source: pa.source, Decision: action, Reason: reason,
	})
	sendStatus(pa.outbox, simpleActionStatus(pa.commandID, actionNotifyUser, pa.deviceID, "",
		pb.ActionPhase_ACTION_PHASE_SUCCESS, reason))
}
