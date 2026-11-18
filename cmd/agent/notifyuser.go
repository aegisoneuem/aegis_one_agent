package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	"github.com/aegisoneuem/aegis_one_agent/internal/uimsg"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

const actionNotifyUser = "notify_user"

// notifyUserPayload is the signed command payload for a user notification.
//   - kind "info" (default) → tray balloon, auto-dismiss, no response.
//   - kind "message"        → a window the user reads and closes, no response.
//   - kind "action"         → an action request with buttons/deadline/snooze; the
//     user's choice is reported back and may trigger on_accept (e.g. reboot).
type notifyUserPayload struct {
	Kind         string   `json:"kind"`
	Title        string   `json:"title"`
	Text         string   `json:"text"`
	Detail       string   `json:"detail"`
	Buttons      []string `json:"buttons"`
	DeadlineUnix int64    `json:"deadline_unix"`
	AllowSnooze  bool     `json:"allow_snooze"`
	SnoozeOpts   []int64  `json:"snooze_opts"`
	ConfirmText  string   `json:"confirm_text"`
	OnAccept     string   `json:"on_accept"` // local action for the primary button: "reboot" (else none)
}

// pendingUIAction remembers an outstanding action so the user's later response can be
// reported against the originating command, re-shown on snooze, and acted upon.
type pendingUIAction struct {
	commandID   string
	deviceID    string
	outbox      chan<- *pb.AgentMessage
	actor       string
	source      string
	msg         uimsg.Message // the message to re-show on snooze
	onAccept    string        // "reboot" etc.
	acceptLabel string        // the primary (accept) button label
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
		Kind: kind, ID: cmd.CommandId, Title: p.Title, Text: p.Text, Detail: p.Detail,
		Buttons: p.Buttons, DeadlineUnix: p.DeadlineUnix,
		AllowSnooze: p.AllowSnooze, SnoozeOpts: p.SnoozeOpts, ConfirmText: p.ConfirmText,
	}

	// For an action we must wait for the user's choice before final status; register
	// it (keyed by command id) so the response maps back, snooze can re-show it, and
	// on_accept can run.
	if kind == uimsg.KindAction {
		accept := ""
		if len(p.Buttons) > 0 {
			accept = p.Buttons[0]
		}
		pendingMu.Lock()
		pendingActions[cmd.CommandId] = &pendingUIAction{
			commandID: cmd.CommandId, deviceID: d.cfg.DeviceID, outbox: d.outbox,
			actor: cmd.Actor, source: cmd.Source, msg: msg, onAccept: strings.ToLower(p.OnAccept), acceptLabel: accept,
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
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionNotifyUser, d.cfg.DeviceID, "",
			pb.ActionPhase_ACTION_PHASE_RUNNING, fmt.Sprintf("action shown to %d user session(s) — awaiting response", n)))
		return
	}
	sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionNotifyUser, d.cfg.DeviceID, "",
		pb.ActionPhase_ACTION_PHASE_SUCCESS, fmt.Sprintf("notification delivered to %d connected user session(s)", n)))
}

// reportUserResponse maps a UI response to its action, reports it (audited), re-shows
// it after a snooze, and runs any on_accept action (e.g. reboot). Called from the
// pipe reader.
func reportUserResponse(id, action string, snoozeSecs int64) {
	pendingMu.Lock()
	pa := pendingActions[id]
	pendingMu.Unlock()
	if pa == nil {
		return // unknown / already-terminal action
	}

	// Snooze: report it, keep it pending, and re-show after the chosen interval.
	if action == "snoozed" && snoozeSecs > 0 {
		reason := fmt.Sprintf("user snoozed for %ds — will re-prompt", snoozeSecs)
		recordAuditEvent(context.Background(), pa.outbox, auditlog.Event{
			DeviceID: pa.deviceID, EventType: "user_action_response", CommandID: pa.commandID,
			Actor: pa.actor, Source: pa.source, Decision: "snoozed", Reason: reason,
		})
		sendStatus(pa.outbox, simpleActionStatus(pa.commandID, actionNotifyUser, pa.deviceID, "",
			pb.ActionPhase_ACTION_PHASE_RUNNING, reason))
		m := pa.msg
		time.AfterFunc(time.Duration(snoozeSecs)*time.Second, func() { deliverUIMessage(m) })
		return
	}

	// Terminal choice — clear the pending entry.
	pendingMu.Lock()
	delete(pendingActions, id)
	pendingMu.Unlock()

	reason := "user chose: " + action
	recordAuditEvent(context.Background(), pa.outbox, auditlog.Event{
		DeviceID: pa.deviceID, EventType: "user_action_response", CommandID: pa.commandID,
		Actor: pa.actor, Source: pa.source, Decision: action, Reason: reason,
	})
	sendStatus(pa.outbox, simpleActionStatus(pa.commandID, actionNotifyUser, pa.deviceID, "",
		pb.ActionPhase_ACTION_PHASE_SUCCESS, reason))

	// Run the local action bound to the accept button (user said yes).
	if pa.onAccept == "reboot" && action == pa.acceptLabel {
		slog.Warn("notify_user: user accepted — scheduling reboot", "command_id", pa.commandID)
		go handleReboot(&pb.RebootSpec{DelaySeconds: 60, Message: "AegisOne: restart requested by administrator"})
	}
}
