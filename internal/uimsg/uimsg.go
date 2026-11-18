// Package uimsg defines the message protocol between the Aegis One service and the
// tray UI (AegisOneClientUI.exe) over a local named pipe. Shared by both sides so the
// wire format never drifts. Pure Go, cross-platform (the pipe transport is
// Windows-specific; the message types are not).
package uimsg

// PipeName is the base name of the UI pipe. The Windows path is \\.\pipe\<PipeName>.
const PipeName = "AegisOneClientUI"

// Kind is the message type.
type Kind string

const (
	// KindNotify: one-way informational message → shown as a tray balloon.
	KindNotify Kind = "notify"
	// KindPrompt: a message that asks the user to choose (Step 3) → shown as a dialog.
	KindPrompt Kind = "prompt"
	// KindResponse: UI → service, the user's choice for a prompt (Step 3).
	KindResponse Kind = "response"
)

// Message is one line of JSON on the pipe.
type Message struct {
	Kind    Kind     `json:"kind"`
	ID      string   `json:"id,omitempty"`      // correlates a prompt with its response
	Title   string   `json:"title,omitempty"`
	Text    string   `json:"text,omitempty"`
	Buttons []string `json:"buttons,omitempty"` // prompt choices (Step 3)
	Action  string   `json:"action,omitempty"`  // response: which button / "dismissed"
}
