package logging

import (
	"log/slog"
	"testing"
)

// SetLevel changes the process-wide level at runtime (the A3 policy logging.level
// override path), and an unrecognized name falls back to info so a bad policy
// value can never silence or over-verbose the agent.
func TestSetLevel_RuntimeOverride(t *testing.T) {
	t.Cleanup(func() { SetLevel("info") })

	SetLevel("info")
	if CurrentLevel() != slog.LevelInfo {
		t.Fatalf("want info, got %v", CurrentLevel())
	}
	SetLevel("debug")
	if CurrentLevel() != slog.LevelDebug {
		t.Fatalf("want debug, got %v", CurrentLevel())
	}
	SetLevel("trace")
	if CurrentLevel() != LevelTrace {
		t.Fatalf("want trace, got %v", CurrentLevel())
	}
	SetLevel("not-a-level") // unknown -> info
	if CurrentLevel() != slog.LevelInfo {
		t.Fatalf("unknown level must fall back to info, got %v", CurrentLevel())
	}
}
