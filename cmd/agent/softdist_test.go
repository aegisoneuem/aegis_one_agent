package main

import (
	"context"
	"errors"
	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	"strings"
	"testing"
	"time"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

func TestPinnedUninstallRetainsTargetAfterDisplayNameDisappears(t *testing.T) {
	original := &pb.SoftwareDeploySpec{Action: "uninstall", DisplayName: "Acme", Version: "1.0"}
	pinned := pinnedRegistryUninstall(original, &softdist.InstalledApp{Key: "Acme-1", RegistryView: "HKLM32"})
	if pinned.UninstallKey != `HKLM32\Acme-1` || pinned.DisplayName != "" || pinned.Version != "1.0" {
		t.Fatalf("wrong identity %+v", pinned)
	}
	if original.UninstallKey != "" || original.DisplayName != "Acme" {
		t.Fatal("dispatch modified the received spec")
	}
}

func TestCustomUninstallPayloadTakesPrecedence(t *testing.T) {
	for _, spec := range []*pb.SoftwareDeploySpec{
		{InstallerType: "command", FilePath: `C:\Acme\custom.exe`, UninstallKey: "Acme"},
		{InstallerType: "command", UninstallKey: "Acme"}, // reject missing command, don't fall back
		{InstallerType: "exe", FilePath: `C:\Acme\remove.exe`, UninstallKey: "Acme"},
		{InstallerType: "ps1", ContentId: "custom-removal", UninstallKey: "Acme"},
		{InstallerType: "msi", FilePath: `C:\Acme\app.msi`, ProductCode: "{GUID}"},
	} {
		if !hasExplicitSoftwarePayload(spec) {
			t.Fatalf("custom payload would use automatic uninstall: %+v", spec)
		}
	}
	if hasExplicitSoftwarePayload(&pb.SoftwareDeploySpec{Action: "uninstall", UninstallKey: "Acme"}) {
		t.Fatal("fileless registry removal should remain available")
	}
}

func TestSoftdistPostVerification(t *testing.T) {
	for _, tc := range []struct {
		name, action, version, installed string
		present                          bool
		detectionErr                     error
		want                             bool
	}{
		{"removed", "uninstall", "", "", false, nil, true},
		{"still registered", "uninstall", "", "1.0", true, nil, false},
		{"registry inaccessible", "uninstall", "", "", false, errors.New("access denied"), false},
		{"old version", "install", "2.0", "1.0", true, nil, false},
		{"unknown version", "install", "2.0", "", true, nil, false},
		{"version verified", "install", "2.0", "2.0", true, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := &pb.SoftwareDeploySpec{Action: tc.action, UninstallKey: "fixture", Version: tc.version}
			ok, note := verifyDetectedOutcome(context.Background(), spec, tc.action, 1, 0, func(*pb.SoftwareDeploySpec) (bool, string, error) { return tc.present, tc.installed, tc.detectionErr })
			if ok != tc.want || note == "" {
				t.Fatalf("ok=%v note=%s", ok, note)
			}
		})
	}
}

func TestSoftdistDetachedUninstallSettle(t *testing.T) {
	calls := 0
	spec := &pb.SoftwareDeploySpec{Action: "uninstall", UninstallKey: "fixture"}
	ok, note := verifyDetectedOutcome(context.Background(), spec, "uninstall", 3, 0, func(*pb.SoftwareDeploySpec) (bool, string, error) { calls++; return calls < 3, "", nil })
	if !ok || calls != 3 || note != "verified removed" {
		t.Fatalf("ok=%v calls=%d note=%s", ok, calls, note)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ok, note = verifyDetectedOutcome(ctx, spec, "uninstall", 3, time.Hour, func(*pb.SoftwareDeploySpec) (bool, string, error) { return true, "", nil })
	if ok || !strings.Contains(note, "cancelled") {
		t.Fatalf("ok=%v note=%s", ok, note)
	}
}
