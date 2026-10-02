package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// dailyWriter writes to a date-stamped log file (e.g. agent-2026-10-02.log) and
// rolls over to a new file when the calendar day changes. Files older than the
// retention window are pruned. It is safe for concurrent use (slog writes from
// multiple goroutines).
type dailyWriter struct {
	dir     string
	base    string // file stem, e.g. "agent"
	ext     string // extension incl. dot, e.g. ".log"
	maxDays int    // retention: delete dated files older than this

	mu     sync.Mutex
	curDay string
	f      *os.File
}

// newDailyWriter derives the date-stamped naming from a base path such as
// "C:\...\logs\agent.log" → "C:\...\logs\agent-YYYY-MM-DD.log".
func newDailyWriter(path string, maxDays int) (*dailyWriter, error) {
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	ext := filepath.Ext(name)
	base := name[:len(name)-len(ext)]
	if base == "" {
		base = "agent"
	}
	w := &dailyWriter{dir: dir, base: base, ext: ext, maxDays: maxDays}
	if err := w.openFor(time.Now()); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *dailyWriter) fileName(day string) string {
	return filepath.Join(w.dir, fmt.Sprintf("%s-%s%s", w.base, day, w.ext))
}

// openFor switches to the file for the given day, closing the previous one and
// pruning old files. Caller holds the lock (or it is construction time).
func (w *dailyWriter) openFor(t time.Time) error {
	day := t.Format("2006-01-02")
	if w.f != nil {
		w.f.Close()
	}
	f, err := os.OpenFile(w.fileName(day), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open daily log %q: %w", w.fileName(day), err)
	}
	w.f = f
	w.curDay = day
	w.prune()
	return nil
}

func (w *dailyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if day := time.Now().Format("2006-01-02"); day != w.curDay {
		if err := w.openFor(time.Now()); err != nil {
			return 0, err
		}
	}
	return w.f.Write(p)
}

func (w *dailyWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f != nil {
		return w.f.Close()
	}
	return nil
}

// prune deletes dated log files older than the retention window. Best-effort:
// errors are ignored so logging never fails because cleanup could not run.
func (w *dailyWriter) prune() {
	if w.maxDays <= 0 {
		return
	}
	matches, err := filepath.Glob(filepath.Join(w.dir, w.base+"-*"+w.ext))
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -w.maxDays)
	for _, m := range matches {
		name := filepath.Base(m)
		dayStr := name[len(w.base)+1 : len(name)-len(w.ext)] // between "base-" and ext
		day, err := time.Parse("2006-01-02", dayStr)
		if err != nil {
			continue // not one of our dated files
		}
		if day.Before(cutoff) {
			os.Remove(m)
		}
	}
}
