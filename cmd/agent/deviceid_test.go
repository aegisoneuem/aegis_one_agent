package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestResolveDeviceID_Override(t *testing.T) {
	dir := t.TempDir()
	id, source, err := resolveDeviceID("  pinned-123  ", dir, "smbios:AAA")
	if err != nil {
		t.Fatal(err)
	}
	if id != "pinned-123" || source != "config override" {
		t.Fatalf("override: got id=%q source=%q", id, source)
	}
	if _, err := os.Stat(filepath.Join(dir, "identity.json")); !os.IsNotExist(err) {
		t.Fatalf("override should not persist identity.json")
	}
}

func TestResolveDeviceID_GenerateThenReuse(t *testing.T) {
	dir := t.TempDir()
	fp := "smbios:SAME-UUID"

	id1, source1, err := resolveDeviceID("", dir, fp)
	if err != nil {
		t.Fatal(err)
	}
	if source1 != "generated" {
		t.Fatalf("first call: source=%q, want generated", source1)
	}
	if !uuidV4.MatchString(id1) {
		t.Fatalf("generated id %q is not a v4 UUID", id1)
	}

	// Same fingerprint → same persisted id.
	id2, source2, err := resolveDeviceID("", dir, fp)
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 || source2 != "persisted" {
		t.Fatalf("reuse: got id=%q source=%q, want %q/persisted", id2, source2, id1)
	}
}

func TestResolveDeviceID_CloneReimageRegenerates(t *testing.T) {
	dir := t.TempDir()

	id1, _, err := resolveDeviceID("", dir, "smbios:MACHINE-A")
	if err != nil {
		t.Fatal(err)
	}

	// Same identity.json, but the hardware fingerprint changed (clone/reimage) →
	// a FRESH id must be issued, not the baked-in one.
	id2, source2, err := resolveDeviceID("", dir, "smbios:MACHINE-B")
	if err != nil {
		t.Fatal(err)
	}
	if id2 == id1 {
		t.Fatalf("clone: id should change when fingerprint changes (got same %q)", id1)
	}
	if source2 != "regenerated (hardware changed — clone/reimage)" {
		t.Fatalf("clone: source=%q", source2)
	}

	// New id is now persisted under MACHINE-B → reused on the next boot.
	id3, source3, err := resolveDeviceID("", dir, "smbios:MACHINE-B")
	if err != nil {
		t.Fatal(err)
	}
	if id3 != id2 || source3 != "persisted" {
		t.Fatalf("after clone: got id=%q source=%q, want %q/persisted", id3, source3, id2)
	}
}

func TestResolveDeviceID_MissingFingerprintReusesID(t *testing.T) {
	dir := t.TempDir()
	id1, _, err := resolveDeviceID("", dir, "smbios:MACHINE-A")
	if err != nil {
		t.Fatal(err)
	}
	// Fingerprint unreadable this boot ("") → do NOT regenerate; reuse persisted id.
	id2, source2, err := resolveDeviceID("", dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 || source2 != "persisted" {
		t.Fatalf("missing fp: got id=%q source=%q, want %q/persisted", id2, source2, id1)
	}
}

func TestNewUUID_Unique(t *testing.T) {
	a, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two UUIDs collided: %q", a)
	}
	for _, u := range []string{a, b} {
		if !uuidV4.MatchString(u) {
			t.Fatalf("not a v4 UUID: %q", u)
		}
	}
}
