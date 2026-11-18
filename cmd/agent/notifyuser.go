package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	n := showUserNotification(p.Title, p.Text)
	// Report the actual reach so a "success" with 0 sessions is visible (no tray UI
	// connected) rather than silently looking delivered.
	phase := pb.ActionPhase_ACTION_PHASE_SUCCESS
	reason := fmt.Sprintf("notification delivered to %d connected user session(s)", n)
	if n == 0 {
		reason = "no tray UI connected — notification not shown (user not logged on, or UI not running)"
	}
	sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionNotifyUser, d.cfg.DeviceID, "", phase, reason))
}
