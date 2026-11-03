package capabilities

import (
	"sort"
	"strings"
	"testing"
)

func TestSchemaVersion(t *testing.T) {
	if got := SchemaVersion(); got != "1" {
		t.Errorf("SchemaVersion() = %q, want %q", got, "1")
	}
}

func TestDeviceType(t *testing.T) {
	if got := DeviceType(); got != "endpoint" {
		t.Errorf("DeviceType() = %q, want %q (Phase-1 default)", got, "endpoint")
	}
}

func TestListIsDefensiveCopy(t *testing.T) {
	a := List()
	b := List()
	if len(a) == 0 {
		t.Fatal("List() returned empty slice")
	}
	a[0] = "SENTINEL_MUTATED"
	if b[0] == "SENTINEL_MUTATED" || supported[0] == "SENTINEL_MUTATED" {
		t.Error("List() must return a defensive copy; mutation of a leaked to b or to the package slice")
	}
}

func TestListHasCoreTaskTypes(t *testing.T) {
	have := make(map[string]bool, len(supported))
	for _, s := range supported {
		have[s] = true
	}
	// A short sanity list of the task types the dispatcher registers today.
	// Adding a new dispatch handler without updating capabilities will trip
	// the paired cross-check test in cmd/agent (TestCapabilitiesCoverDispatcher).
	core := []string{
		"software_deploy", "install_patch", "uninstall_patch", "scan_now",
		"reboot", "run_command", "open_shell", "collect_file", "report_device_state",
	}
	for _, want := range core {
		if !have[want] {
			t.Errorf("capabilities.List() missing core task type %q", want)
		}
	}
}

func TestListIsSortedForReadability(t *testing.T) {
	// We don't REQUIRE alphabetical order; task types are grouped by family
	// (SoftDist / Patch / Lifecycle / NSH / FeatureFlags). This test just
	// pins that no entry is a trailing whitespace / empty / duplicate.
	seen := make(map[string]bool, len(supported))
	for _, s := range supported {
		if strings.TrimSpace(s) == "" {
			t.Errorf("capabilities.supported has an empty/whitespace entry")
		}
		if s != strings.TrimSpace(s) {
			t.Errorf("capabilities.supported[%q] has leading/trailing whitespace", s)
		}
		if seen[s] {
			t.Errorf("capabilities.supported has duplicate entry %q", s)
		}
		seen[s] = true
	}
	// Also assert the family-grouping intent: when sorted, we should see
	// clear clusters. We just check that we have at least a handful of
	// entries across the alphabet.
	sorted := List()
	sort.Strings(sorted)
	if len(sorted) < 15 {
		t.Errorf("capabilities list unexpectedly short: %d entries", len(sorted))
	}
}
