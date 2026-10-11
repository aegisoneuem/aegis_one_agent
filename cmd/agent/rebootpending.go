package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// rebootPendingStore persists the set of KBs the agent installed that returned
// "reboot required" and are still awaiting a reboot. It gives the console per-patch
// attribution for a device's pending-reboot state (which patches are waiting) —
// Windows' global pending-reboot flags don't say which KB, so we track the ones WE
// installed (best-effort, like other UEM agents attribute to their own actions).
// Plaintext (KB numbers are public). Cleared once the OS pending-reboot flag is
// gone (the reboot happened, so those patches are now effective).
type rebootPendingStore struct {
	path string
	mu   sync.Mutex
}

func newRebootPendingStore(dir string) *rebootPendingStore {
	return &rebootPendingStore{path: filepath.Join(dir, "reboot-pending.json")}
}

func (s *rebootPendingStore) add(kb string) {
	kb = strings.TrimSpace(kb)
	if kb == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.loadSet()
	set[kb] = struct{}{}
	s.saveSet(set)
}

func (s *rebootPendingStore) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.loadSet()
	out := make([]string, 0, len(set))
	for kb := range set {
		out = append(out, kb)
	}
	sort.Strings(out)
	return out
}

// clear removes the store (called once the OS reports no pending reboot).
func (s *rebootPendingStore) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = os.Remove(s.path)
}

func (s *rebootPendingStore) loadSet() map[string]struct{} {
	set := map[string]struct{}{}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return set
	}
	var kbs []string
	if json.Unmarshal(data, &kbs) == nil {
		for _, kb := range kbs {
			if kb = strings.TrimSpace(kb); kb != "" {
				set[kb] = struct{}{}
			}
		}
	}
	return set
}

func (s *rebootPendingStore) saveSet(set map[string]struct{}) {
	kbs := make([]string, 0, len(set))
	for kb := range set {
		kbs = append(kbs, kb)
	}
	sort.Strings(kbs)
	data, err := json.MarshalIndent(kbs, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, s.path)
	}
}
