package main

import (
	"os"
	"path/filepath"
	"strings"
)

// Post-reboot continuation: when the agent schedules a reboot to finish applying
// updates, it drops a small marker in the secure dir. On the next startup the agent
// finds the marker and shows the user a "your computer was restarted to finish
// installing updates" message, then clears it. The marker's presence is the signal;
// its content is an optional detail line for the message.

func rebootNoticePath(secureDir string) string { return filepath.Join(secureDir, ".rebootnotice") }

// writeRebootNotice records that a reboot was scheduled (detail is optional context,
// e.g. the KBs). Best effort — a missing marker just means no post-reboot message.
func writeRebootNotice(secureDir, detail string) {
	_ = os.WriteFile(rebootNoticePath(secureDir), []byte(detail), 0o600)
}

// consumeRebootNotice returns (detail, true) if a marker exists, deleting it so the
// message shows exactly once.
func consumeRebootNotice(secureDir string) (string, bool) {
	p := rebootNoticePath(secureDir)
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	_ = os.Remove(p)
	return strings.TrimSpace(string(b)), true
}
