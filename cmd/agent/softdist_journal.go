package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A durable job journal records every in-flight software_deploy job on disk so
// the agent can report honestly after a crash, power loss, service restart or
// forced kill. Each worker transition writes a short JSON record; a terminal
// finish deletes it. On startup the installer scans the journal for records
// that never reached a terminal state, emits a RECOVERED_FAILED ActionStatus
// so the console learns the job did not complete, and removes orphan
// extraction directories so disk does not leak.
//
// The journal is intentionally narrow in scope: it tracks CORRELATION and
// CLEANUP state. It is NOT a resume-at-the-exact-byte queue — Windows
// installers are not safely resumable at arbitrary mid-points, so recovery
// reports failure rather than silently retrying.

type jobJournal struct {
	dir string
	mu  sync.Mutex
}

type journalState string

const (
	journalQueued     journalState = "queued"
	journalExtracting journalState = "extracting"
	journalRunning    journalState = "running"
	journalVerifying  journalState = "verifying"
	// Terminal states exist only transiently — the record is deleted on
	// finish(). They are listed here so a crash between write-and-delete can
	// still be classified cleanly by the recovery sweep.
	journalCompleted journalState = "completed"
)

// journalEntry is the on-disk record. Keep it small and self-describing so a
// recovery pass needs no additional context to classify + clean up.
type journalEntry struct {
	CommandID    string       `json:"command_id"`
	DeviceID     string       `json:"device_id"`
	Name         string       `json:"name"`
	Action       string       `json:"action"`
	State        journalState `json:"state"`
	PackageDir   string       `json:"package_dir,omitempty"`
	StartedAt    int64        `json:"started_at"`
	LastUpdated  int64        `json:"last_updated"`
	AgentVersion string       `json:"agent_version,omitempty"`
}

func newJobJournal(secureDir string) *jobJournal {
	return &jobJournal{dir: filepath.Join(secureDir, "jobs")}
}

func (j *jobJournal) ensureDir() error {
	if j == nil {
		return errors.New("journal not configured")
	}
	return os.MkdirAll(j.dir, 0o700)
}

func (j *jobJournal) path(commandID string) string {
	return filepath.Join(j.dir, sanitize(commandID)+".state")
}

// write persists a single transition. Atomic (write-temp + rename) so a crash
// mid-write never leaves a half-parsed JSON that the recovery sweep would skip
// or misread. Each caller passes the whole entry — the journal does not merge.
func (j *jobJournal) write(entry *journalEntry) error {
	if j == nil || entry == nil {
		return nil
	}
	if err := j.ensureDir(); err != nil {
		return err
	}
	entry.LastUpdated = time.Now().Unix()
	j.mu.Lock()
	defer j.mu.Unlock()
	full := j.path(entry.CommandID)
	tmp := full + ".tmp"
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("journal marshal %s: %w", entry.CommandID, err)
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("journal write %s: %w", entry.CommandID, err)
	}
	if err := os.Rename(tmp, full); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("journal rename %s: %w", entry.CommandID, err)
	}
	return nil
}

// read returns the entry for a command, or nil if none is present. ErrNotExist
// is translated to a nil entry so callers can treat "no record" as the normal
// first-write case.
func (j *jobJournal) read(commandID string) (*journalEntry, error) {
	if j == nil {
		return nil, nil
	}
	data, err := os.ReadFile(j.path(commandID))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	entry := &journalEntry{}
	if err := json.Unmarshal(data, entry); err != nil {
		return nil, fmt.Errorf("journal parse %s: %w", commandID, err)
	}
	return entry, nil
}

// delete removes the entry on terminal finish. Missing is not an error — the
// terminal path is called in a defer and may run after a prior write error.
func (j *jobJournal) delete(commandID string) error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	err := os.Remove(j.path(commandID))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// recover scans the journal directory for in-flight entries. Each returned
// entry is one job that did NOT reach terminal state before the agent stopped,
// so the caller must emit a RECOVERED_FAILED ActionStatus and sweep the
// orphaned package directory. Entries whose JSON is corrupt are logged and
// skipped — they are deleted so recovery is self-healing.
func (j *jobJournal) recover() ([]*journalEntry, error) {
	if j == nil {
		return nil, nil
	}
	if err := j.ensureDir(); err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(j.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []*journalEntry
	for _, ent := range ents {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		// Clean up stale half-written tmp files from a crash mid-write.
		if strings.HasSuffix(name, ".tmp") {
			_ = os.Remove(filepath.Join(j.dir, name))
			continue
		}
		if !strings.HasSuffix(name, ".state") {
			continue
		}
		full := filepath.Join(j.dir, name)
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		entry := &journalEntry{}
		if err := json.Unmarshal(data, entry); err != nil {
			// Corrupt entry — delete rather than keep blocking future sweeps.
			_ = os.Remove(full)
			continue
		}
		// Completed entries should have been deleted; a leftover one is still
		// a leak signal — report it so the operator can see the mismatch.
		out = append(out, entry)
	}
	return out, nil
}
