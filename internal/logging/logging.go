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

	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

// Custom levels extending slog's built-in DEBUG(-4)/INFO(0)/WARN(4)/ERROR(8).
const (
	LevelTrace    = slog.Level(-8) // most verbose: full command output, each step
	LevelCritical = slog.Level(12) // above ERROR: agent-down / unrecoverable
)

// Rotation holds log-file rotation settings (used only when a log file is set).
type Rotation struct {
	MaxSizeMB  int // rotate after the file reaches this size
	MaxAgeDays int // delete rotated files older than this (retention)
	MaxBackups int // keep at most this many rotated files
}

// Init configures the process-wide slog default logger. When file is non-empty,
// logs are written to both stdout and a size/age-rotated file. It returns an
// io.Closer for the file sink (nil when logging only to stdout).
func Init(level, file string, rot Rotation) (io.Closer, error) {
	var w io.Writer = os.Stdout
	var closer io.Closer

	if strings.TrimSpace(file) != "" {
		if dir := filepath.Dir(file); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("could not create log directory %q: %w", dir, err)
			}
		}
		lj := &lumberjack.Logger{
			Filename:   file,
			MaxSize:    maxOr(rot.MaxSizeMB, 20), // MB
			MaxAge:     maxOr(rot.MaxAgeDays, 10), // days (retention)
			MaxBackups: maxOr(rot.MaxBackups, 10),
			Compress:   true,
		}
		closer = lj
		w = io.MultiWriter(os.Stdout, lj)
	}

	handler := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level:       ParseLevel(level),
		ReplaceAttr: renameLevels,
	})
	slog.SetDefault(slog.New(handler))
	return closer, nil
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
