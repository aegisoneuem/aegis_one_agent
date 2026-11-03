package main

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/fileops"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// -----------------------------------------------------------------------------
// NSH path tab-completion (Phase 1b)
// -----------------------------------------------------------------------------
//
// Previously only the top-level verbs auto-completed on Tab. Phase 1b adds
// path completion: when a verb that takes a path arg has been typed and the
// cursor is on the path token, the completer dispatches a file_operation
// list_directory to the currently-cd'd target to enumerate the parent
// directory, then returns the matching entries.
//
// Caching: a one-shot Tab press from an operator exploring a directory
// should not cost multiple round-trips to the agent. A small 5-second TTL
// cache per (device, directory) absorbs rapid Tabs. Cache is per-session
// so a new NSH REPL always starts cold.
//
// Fallback: if the agent is offline, times out, or returns an error, path
// completion returns nothing and the Tab becomes a no-op. Verb completion
// is unaffected either way.

// verbTakesPath returns true when verb accepts a path-shaped argument the
// completer should offer entries for. Keep conservative — a false positive
// causes an unnecessary agent round-trip; a false negative only means Tab
// does nothing (not a bug, just a UX gap).
func verbTakesPath(verb string) bool {
	switch strings.ToLower(strings.TrimSpace(verb)) {
	case "cd", "cat", "grab", "pull", "copy", "cp", "move", "mv", "delete", "del", "rm", "mkdir", "md", "touch":
		return true
	}
	return false
}

// pathComplete completes the LAST token of rest as a filesystem path on
// the currently-cd'd single target. Returns full-line completions the
// liner library will offer the user. On any failure (no target, offline,
// agent error, timeout) returns nil — the Tab becomes a no-op, which is
// the right fallback for an interactive REPL.
func (s *nshSession) completePathArg(prefix, head, rest string) []string {
	if len(s.targets) != 1 {
		return nil
	}
	devID := s.targets[0]
	// Isolate the LAST whitespace-separated token in rest — that is the
	// path being edited. Everything before it is kept as-is in the returned
	// candidates so the operator does not lose typed context.
	cut := strings.LastIndexAny(rest, " \t")
	preserved := rest[:cut+1] // includes the trailing space
	token := rest[cut+1:]
	if strings.HasPrefix(token, `\\`) {
		// UNC form: path on a specific host. Phase-1b completion only
		// supports the current-target form; UNC targets would need an
		// extra deviceIDByHostname step. Skip for now.
		return nil
	}
	dir, partial := splitDirAndPartial(token)
	entries, ok := s.listDirectoryCached(devID, dir)
	if !ok {
		return nil
	}
	var out []string
	plow := strings.ToLower(partial)
	for _, e := range entries {
		if !strings.HasPrefix(strings.ToLower(e.Name), plow) {
			continue
		}
		full := joinWithSep(dir, e.Name, token)
		if e.Directory {
			full += `\`
		}
		out = append(out, head+" "+preserved+full)
	}
	return out
}

// splitDirAndPartial splits "C:\\temp\\par" into dir="C:\\temp\\" and
// partial="par". For a bare "par" with no separators, dir="" (empty =
// current working dir on target).
func splitDirAndPartial(token string) (dir, partial string) {
	i := strings.LastIndexAny(token, `\/`)
	if i < 0 {
		return "", token
	}
	return token[:i+1], token[i+1:]
}

// joinWithSep rebuilds dir+name preserving the separator the operator was
// using (backslash on Windows paths, forward slash on POSIX-style input).
// Keeps the completed suggestion consistent with what the user typed.
func joinWithSep(dir, name, origToken string) string {
	if dir == "" {
		return name
	}
	sep := `\`
	if strings.Contains(origToken, `/`) && !strings.Contains(origToken, `\`) {
		sep = `/`
	}
	return strings.TrimRight(dir, `\/`) + sep + name
}

// -----------------------------------------------------------------------------
// Cache
// -----------------------------------------------------------------------------

type listDirCacheEntry struct {
	entries []fileops.Entry
	fetched time.Time
}

type listDirCache struct {
	mu  sync.Mutex
	byK map[string]listDirCacheEntry
	ttl time.Duration
}

var sharedListDirCache = &listDirCache{
	byK: make(map[string]listDirCacheEntry),
	ttl: 5 * time.Second,
}

func (c *listDirCache) key(devID, dir string) string {
	return devID + "|" + strings.ToLower(dir)
}

func (c *listDirCache) get(devID, dir string) ([]fileops.Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.byK[c.key(devID, dir)]
	if !ok || time.Since(v.fetched) > c.ttl {
		return nil, false
	}
	return v.entries, true
}

func (c *listDirCache) put(devID, dir string, entries []fileops.Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byK[c.key(devID, dir)] = listDirCacheEntry{entries: entries, fetched: time.Now()}
}

// listDirectoryCached returns the directory listing for (devID, dir),
// using the shared 5-second cache. On a miss it dispatches a
// file_operation list_directory to the agent synchronously (short timeout
// so a slow / offline agent does not freeze the REPL Tab).
func (s *nshSession) listDirectoryCached(devID, dir string) ([]fileops.Entry, bool) {
	if dir == "" {
		dir = s.workingDirs[devID]
		if dir == "" {
			dir = `C:\`
		}
	}
	if entries, ok := sharedListDirCache.get(devID, dir); ok {
		return entries, true
	}
	spec := fileops.Spec{
		Version:        1,
		Operation:      "list_directory",
		Source:         dir,
		MaxFiles:       1024,
		TimeoutSeconds: 3,
	}
	payload, err := json.Marshal(spec)
	if err != nil {
		return nil, false
	}
	st, _ := s.sendTaskPayload(devID, "file_operation", payload, "")
	if st == nil || st.Phase != pb.ActionPhase_ACTION_PHASE_SUCCESS {
		return nil, false
	}
	var res fileops.Result
	if err := json.Unmarshal([]byte(st.Detail), &res); err != nil {
		return nil, false
	}
	sharedListDirCache.put(devID, dir, res.Entries)
	return res.Entries, true
}
