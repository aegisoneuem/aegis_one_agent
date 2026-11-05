//go:build windows

package main

import (
	"testing"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// A KB install/uninstall MUST change the fingerprint so the server gets a
// prompt Layer 1 resend and re-diffs — this is the core of the server-side
// detection model.
func TestDeviceStateFingerprint_InstalledKBsDriveChange(t *testing.T) {
	base := &pb.DeviceStateReport{
		Build: "22631.4890", Architecture: "amd64",
		InstalledKbs: []string{"KB5034123", "KB5034765"},
	}
	fpBase := deviceStateFingerprint(base)

	// Add a KB -> must change.
	added := &pb.DeviceStateReport{
		Build: "22631.4890", Architecture: "amd64",
		InstalledKbs: []string{"KB5034123", "KB5034765", "KB5040000"},
	}
	if deviceStateFingerprint(added) == fpBase {
		t.Error("adding an installed KB did not change the fingerprint (server would miss the install)")
	}

	// Remove a KB -> must change.
	removed := &pb.DeviceStateReport{
		Build: "22631.4890", Architecture: "amd64",
		InstalledKbs: []string{"KB5034123"},
	}
	if deviceStateFingerprint(removed) == fpBase {
		t.Error("removing an installed KB did not change the fingerprint")
	}
}

// Identical state (same sorted KB set) must produce an identical fingerprint,
// otherwise every poll would falsely resend. CollectLocalState returns the set
// sorted+deduped, so equal state == equal bytes.
func TestDeviceStateFingerprint_StableWhenUnchanged(t *testing.T) {
	a := &pb.DeviceStateReport{
		Build: "22631.4890", Architecture: "amd64",
		InstalledKbs: []string{"KB5034123", "KB5034765"},
	}
	b := &pb.DeviceStateReport{
		Build: "22631.4890", Architecture: "amd64",
		InstalledKbs: []string{"KB5034123", "KB5034765"},
	}
	if deviceStateFingerprint(a) != deviceStateFingerprint(b) {
		t.Error("identical device state produced different fingerprints (would cause false resends)")
	}
}

// A build bump (the authoritative cumulative level) must still change the
// fingerprint even if the KB list is unchanged.
func TestDeviceStateFingerprint_BuildDrivesChange(t *testing.T) {
	a := &pb.DeviceStateReport{Build: "22631.4890", Architecture: "amd64", InstalledKbs: []string{"KB1"}}
	b := &pb.DeviceStateReport{Build: "22631.5000", Architecture: "amd64", InstalledKbs: []string{"KB1"}}
	if deviceStateFingerprint(a) == deviceStateFingerprint(b) {
		t.Error("build change did not change the fingerprint")
	}
}
