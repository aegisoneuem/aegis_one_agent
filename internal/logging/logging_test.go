package logging

import (
	"log/slog"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"":         slog.LevelInfo,
		"info":     slog.LevelInfo,
		"INFO":     slog.LevelInfo,
		"debug":    slog.LevelDebug,
		"warn":     slog.LevelWarn,
		"warning":  slog.LevelWarn,
		"error":    slog.LevelError,
		"trace":    LevelTrace,
		"critical": LevelCritical,
		"crit":     LevelCritical,
		"bogus":    slog.LevelInfo, // unknown falls back to info
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestLevelOrdering(t *testing.T) {
	if !(LevelTrace < slog.LevelDebug && slog.LevelError < LevelCritical) {
		t.Fatal("custom levels are not ordered correctly")
	}
}

func TestRenameLevels(t *testing.T) {
	a := renameLevels(nil, slog.Attr{Key: slog.LevelKey, Value: slog.AnyValue(LevelTrace)})
	if a.Value.String() != "TRACE" {
		t.Errorf("trace rendered as %q", a.Value.String())
	}
	a = renameLevels(nil, slog.Attr{Key: slog.LevelKey, Value: slog.AnyValue(LevelCritical)})
	if a.Value.String() != "CRITICAL" {
		t.Errorf("critical rendered as %q", a.Value.String())
	}
}
