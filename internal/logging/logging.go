// Package logging provides the process-wide, level-based logger shared by the
// agent and all its modules. It wraps the standard library log/slog so there is
// no heavy dependency, honors AEGIS_LOG_LEVEL, and can tee logs to a rotating
// file for post-mortem troubleshooting.
//
// Levels (low → high): trace < debug < info < warn < error < critical.
// Default is info. trace/debug are intentionally OFF by default — they are
// verbose and may surface sensitive detail — so customers enable them only while
// diagnosing an issue, then revert to info.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Custom levels extending slog's built-in DEBUG(-4)/INFO(0)/WARN(4)/ERROR(8).
const (
	LevelTrace    = slog.Level(-8) // most verbose: full command output, each step
	LevelCritical = slog.Level(12) // above ERROR: agent-down / unrecoverable
)

// Rotation holds log-file rotation settings (used only when a log file is set).
// The agent rotates DAILY: a new date-stamped file per calendar day, keeping
// MaxAgeDays of history.
type Rotation struct {
	MaxAgeDays int // retention: delete dated files older than this (default 10)
}

// Init configures the process-wide slog default logger. When file is non-empty,
// logs are written to both stdout and a DAILY, date-stamped file (e.g.
// agent-2026-10-02.log) that rolls over each day and keeps MaxAgeDays of history.
// It returns an io.Closer for the file sink (nil when logging only to stdout).
func Init(level, file string, rot Rotation) (io.Closer, error) {
	// stdout is wrapped so a failed console write (e.g. a Windows service has no
	// console) is swallowed — otherwise io.MultiWriter would abort on the stdout
	// error and never reach the log file.
	var w io.Writer = tolerant{os.Stdout}
	var closer io.Closer

	if strings.TrimSpace(file) != "" {
		if dir := filepath.Dir(file); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("could not create log directory %q: %w", dir, err)
			}
		}
		dw, err := newDailyWriter(file, maxOr(rot.MaxAgeDays, 10))
		if err != nil {
			return nil, err
		}
		closer = dw
		// Daily file first (the source of truth), then best-effort stdout.
		w = io.MultiWriter(dw, tolerant{os.Stdout})
	}

	handler := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level:       ParseLevel(level),
		ReplaceAttr: renameLevels,
	})
	slog.SetDefault(slog.New(handler))
	return closer, nil
}

// tolerant wraps a writer (stdout) so write errors are swallowed and never
// returned — a MultiWriter aborts on the first error, which would starve the log
// file when stdout is unavailable (e.g. a service with no console).
type tolerant struct{ w io.Writer }

func (t tolerant) Write(p []byte) (int, error) {
	_, _ = t.w.Write(p) // best-effort
	return len(p), nil
}

func maxOr(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

// renameLevels gives the custom levels readable names in output (otherwise slog
// prints e.g. "DEBUG-4" / "ERROR+4").
func renameLevels(groups []string, a slog.Attr) slog.Attr {
	if a.Key != slog.LevelKey {
		return a
	}
	if lvl, ok := a.Value.Any().(slog.Level); ok {
		switch lvl {
		case LevelTrace:
			a.Value = slog.StringValue("TRACE")
		case LevelCritical:
			a.Value = slog.StringValue("CRITICAL")
		}
	}
	return a
}

// ParseLevel maps a level name to a slog.Level, defaulting to info.
func ParseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "trace":
		return LevelTrace
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "critical", "crit", "fatal":
		return LevelCritical
	default:
		return slog.LevelInfo
	}
}

// Trace logs at the most verbose level (full command output, per-step detail).
func Trace(msg string, args ...any) {
	slog.Log(context.Background(), LevelTrace, msg, args...)
}

// Critical logs an unrecoverable/agent-down condition above ERROR.
func Critical(msg string, args ...any) {
	slog.Log(context.Background(), LevelCritical, msg, args...)
}
