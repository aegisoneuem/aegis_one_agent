package main

import (
	"context"
	"encoding/json"
	"strings"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

const actionNotifyUser = "notify_user"

// notifyUserPayload is the signed command payload for a user notification.
type notifyUserPayload struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// handleNotifyUserCmd shows a notification to the logged-on user(s) via the tray UI.
// Accepts either a JSON payload {title,text} or a plain "Title|Text" string.
func (d *dispatcher) handleNotifyUserCmd(ctx context.Context, _ pb.AgentControlPlaneClient, cmd *pb.Command) {
	var p notifyUserPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		parts := strings.SplitN(strings.TrimSpace(string(cmd.Payload)), "|", 2)
		p.Title = strings.TrimSpace(parts[0])
		if len(parts) > 1 {
			p.Text = strings.TrimSpace(parts[1])
		}
	}
	if p.Title == "" {
		p.Title = "AegisOne Client"
	}
	showUserNotification(p.Title, p.Text) // no-op if no UI is connected
	sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionNotifyUser, d.cfg.DeviceID, "",
		pb.ActionPhase_ACTION_PHASE_SUCCESS, "notification delivered to connected user sessions"))
}
