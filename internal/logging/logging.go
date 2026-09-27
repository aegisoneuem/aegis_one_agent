// Package logging provides the process-wide, level-based logger shared by the
// agent and all its modules. It wraps the standard library log/slog so there is
// no external dependency, honors AEGIS_LOG_LEVEL, and can additionally tee logs
// to a file (AEGIS_LOG_FILE) for post-mortem troubleshooting.
//
// Levels: debug < info < warn < error. Default is info. DEBUG is intentionally
// NOT on by default — it is verbose and may surface sensitive detail — so
// customers enable it only while diagnosing an issue, then revert to info.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Init configures the process-wide slog default logger and returns the log file
// handle (or nil when no file was requested) so the caller can Close it on exit.
//
// level is matched case-insensitively; anything unrecognized falls back to info.
// When file is non-empty, logs are written to both stdout and that file.
func Init(level, file string) (*os.File, error) {
	var w io.Writer = os.Stdout
	var f *os.File

	if strings.TrimSpace(file) != "" {
		if dir := filepath.Dir(file); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("could not create log directory %q: %w", dir, err)
			}
		}
		opened, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, fmt.Errorf("could not open log file %q: %w", file, err)
		}
		f = opened
		w = io.MultiWriter(os.Stdout, f)
	}

	handler := slog.NewTextHandler(w, &slog.HandlerOptions{Level: ParseLevel(level)})
	slog.SetDefault(slog.New(handler))
	return f, nil
}

// ParseLevel maps a level name to a slog.Level, defaulting to info.
func ParseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
