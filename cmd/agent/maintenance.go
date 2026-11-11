package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/policy"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

// gatedByMaintenance is the set of STATE-CHANGING command types deferred when the
// active policy has a maintenance window and we are outside it. Read-only and
// control commands (scan, inventory, audit, policy, report, remote_control,
// collect/file ops) always run — a window governs installs and reboots, not
// visibility or management.
var gatedByMaintenance = map[string]bool{
	"install_patch": true, "uninstall_patch": true, "stage_patch": true,
	"software_deploy": true, "software_deploy_v2": true, "software_deploy_bundle": true,
	"software_deploy_workflow": true, "software_deploy_detect": true, "reboot": true,
}

var weekdayAbbr = map[time.Weekday]string{
	time.Sunday: "sun", time.Monday: "mon", time.Tuesday: "tue", time.Wednesday: "wed",
	time.Thursday: "thu", time.Friday: "fri", time.Saturday: "sat",
}

// maintenanceConfigured reports whether a policy actually restricts a window.
// An empty day list AND start==end means "no restriction".
func maintenanceConfigured(m policy.Maintenance) bool {
	return len(m.Days) > 0 || m.StartHour != m.EndHour
}

// inMaintenanceWindow reports whether now (local time) is inside the window. An
// unconfigured window means "always" (no restriction). The hour range is
// [start, end); start>end spans midnight; start==end means no hour restriction.
func inMaintenanceWindow(m policy.Maintenance, now time.Time) bool {
	if !maintenanceConfigured(m) {
		return true
	}
	if len(m.Days) > 0 {
		today := weekdayAbbr[now.Weekday()]
		ok := false
		for _, d := range m.Days {
			if strings.EqualFold(strings.TrimSpace(d), today) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if m.StartHour != m.EndHour {
		h := now.Hour()
		if m.StartHour < m.EndHour {
			if h < m.StartHour || h >= m.EndHour {
				return false
			}
		} else if h >= m.EndHour && h < m.StartHour { // spans midnight: outside = [end, start)
			return false
		}
	}
	return true
}

// policyInMaintenanceWindow checks the active policy's window against now (no
// policy, or no window, => always in-window).
func policyInMaintenanceWindow(now time.Time) bool {
	p := getActivePolicy()
	if p == nil {
		return true
	}
	return inMaintenanceWindow(p.Maintenance, now)
}

// --- persistent deferred-command queue (restart-safe) ---

func deferredDir(secureDir string) string { return filepath.Join(secureDir, "deferred") }

// sanitizeCmdID keeps a command id safe as a filename.
func sanitizeCmdID(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "cmd"
	}
	s := b.String()
	if len(s) > 128 {
		s = s[:128]
	}
	return s
}

// persistDeferredCommand writes a (already signature-verified) command to the
// deferred queue so it survives restart and runs when the window opens.
func persistDeferredCommand(secureDir string, cmd *pb.Command) error {
	dir := deferredDir(secureDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := proto.Marshal(cmd)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, sanitizeCmdID(cmd.CommandId)+".cmd"), raw, 0o600)
}

// loadDeferredCommands returns the queued commands (best-effort; a corrupt entry
// is skipped, not fatal).
func loadDeferredCommands(secureDir string) []*pb.Command {
	entries, err := os.ReadDir(deferredDir(secureDir))
	if err != nil {
		return nil
	}
	var out []*pb.Command
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".cmd") {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join(deferredDir(secureDir), e.Name()))
		if rerr != nil {
			continue
		}
		var cmd pb.Command
		if proto.Unmarshal(raw, &cmd) == nil && cmd.CommandId != "" {
			out = append(out, &cmd)
		}
	}
	return out
}

func removeDeferredCommand(secureDir, commandID string) {
	_ = os.Remove(filepath.Join(deferredDir(secureDir), sanitizeCmdID(commandID)+".cmd"))
}
