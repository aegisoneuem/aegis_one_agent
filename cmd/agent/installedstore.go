package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// installedStore persists the AUTHORITATIVE set of installed KBs from the most
// recent full compliance scan (WUA IsInstalled=1). The install worker uses it as
// a RELIABLE "already installed?" pre-check — the cheap CBS registry check misses
// some updates (e.g. cumulative/SSU packages), so relying on CBS alone makes the
// agent attempt a redundant install of an already-present patch. The scan list is
// the ground truth; it is refreshed after every servicing change (the CBS watch
// re-triggers a scan on install/uninstall), so it stays current — not just weekly.
//
// Plaintext (KB numbers are public). Stored normalized (digits only, no "KB").
type installedStore struct {
	path string
	mu   sync.Mutex
}

func newInstalledStore(dir string) *installedStore {
	return &installedStore{path: filepath.Join(dir, "installed-kbs.json")}
}

// replace overwrites the whole set with the latest scan's installed KBs. A full
// replace (not merge) is correct: the scan is authoritative, so a KB that was
// uninstalled since the last scan is correctly dropped.
func (s *installedStore) replace(kbs []string) {
	if s == nil {
		return
	}
	set := map[string]struct{}{}
	for _, kb := range kbs {
		if n := normalizeKB(kb); n != "" {
			set[n] = struct{}{}
		}
	}
	norm := make([]string, 0, len(set))
	for kb := range set {
		norm = append(norm, kb)
	}
	sort.Strings(norm)

	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(norm, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

// remove drops a KB from the set immediately — called right after a successful
// uninstall so a quick reinstall is NOT wrongly skipped as "already installed"
// before the next scan refreshes the set (the scan is ~minutes away).
func (s *installedStore) remove(kb string) {
	if s == nil {
		return
	}
	drop := normalizeKB(kb)
	if drop == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var kbs []string
	if json.Unmarshal(data, &kbs) != nil {
		return
	}
	out := make([]string, 0, len(kbs))
	for _, k := range kbs {
		if normalizeKB(k) != drop {
			out = append(out, k)
		}
	}
	newData, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, newData, 0o644) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

// has reports whether the KB is in the last scan's installed set. False when no
// scan has run yet (the caller then falls back to the CBS check / the installer's
// own already-installed detection).
func (s *installedStore) has(kb string) bool {
	if s == nil {
		return false
	}
	want := normalizeKB(kb)
	if want == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return false
	}
	var kbs []string
	if json.Unmarshal(data, &kbs) != nil {
		return false
	}
	for _, k := range kbs {
		if normalizeKB(k) == want {
			return true
		}
	}
	return false
}
