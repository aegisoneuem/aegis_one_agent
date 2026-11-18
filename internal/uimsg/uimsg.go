// Package uimsg defines the message protocol between the Aegis One service and the
// tray UI (AegisOneClientUI.exe) over a local named pipe. Shared by both sides so the
// wire format never drifts. Pure Go, cross-platform (the pipe transport is
// Windows-specific; the message types are not).
package uimsg

// PipeName is the base name of the UI pipe. The Windows path is \\.\pipe\<PipeName>.
const PipeName = "AegisOneClientUI"

// Kind is the message type — it maps to how the UI renders it (BigFix taxonomy).
type Kind string

const (
	// KindNotify: one-way info → tray balloon, auto-dismisses after a few seconds.
	KindNotify Kind = "notify"
	// KindMessage: a window with text the user READS and closes with X. Persistent
	// (does not auto-dismiss). No response expected.
	KindMessage Kind = "message"
	// KindAction: an action request → a prominent window with Title/Text, an optional
	// deadline, choice Buttons, and an optional Snooze. The user's choice comes back
	// as a KindResponse (correlated by ID).
	KindAction Kind = "action"
	// KindResponse: UI → service, the user's choice for a KindAction.
	KindResponse Kind = "response"
)

// Message is one line of JSON on the pipe.
type Message struct {
	Kind         Kind     `json:"kind"`
	ID           string   `json:"id,omitempty"`            // correlates an action with its response
	Title        string   `json:"title,omitempty"`
	Text         string   `json:"text,omitempty"`
	Detail       string   `json:"detail,omitempty"`        // action: secondary line (e.g. "This reboot applies: <patch>")
	Buttons      []string `json:"buttons,omitempty"`       // action choices, e.g. ["Take Action","Cancel"]
	DeadlineUnix int64    `json:"deadline_unix,omitempty"` // action: auto-run deadline (0 = none)
	AllowSnooze  bool     `json:"allow_snooze,omitempty"`  // action: offer a Snooze control
	SnoozeOpts   []int64  `json:"snooze_opts,omitempty"`   // action: snooze durations (seconds) for the dropdown
	ConfirmText  string   `json:"confirm_text,omitempty"`  // action: if set, the primary button opens a confirmation dialog first
	Action       string   `json:"action,omitempty"`        // response: chosen button / "snoozed" / "dismissed" / "cancelled"
	SnoozeSecs   int64    `json:"snooze_secs,omitempty"`   // response: chosen snooze duration
}
