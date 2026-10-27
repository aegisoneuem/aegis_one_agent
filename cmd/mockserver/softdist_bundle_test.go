package main

import (
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"testing"
)

func TestMockBundleSettings(t *testing.T) {
	for _, tc := range []struct {
		env          map[string]string
		typ          string
		fail, bundle bool
	}{
		{map[string]string{}, "command", false, false},
		{map[string]string{"AEGIS_DEPLOY_BUNDLE_COMMAND": "cmd.exe"}, "command", false, true},
		{map[string]string{"AEGIS_DEPLOY_BUNDLE_ENTRY": "setup.msi"}, "msi", false, true},
		{map[string]string{"AEGIS_DEPLOY_BUNDLE_ENTRY": "../escape"}, "msi", true, false},
		{map[string]string{"AEGIS_DEPLOY_BUNDLE_COMMAND": "cmd.exe", "AEGIS_DEPLOY_BUNDLE_CLEANUP": "retain"}, "command", true, false},
	} {
		spec := &pb.SoftwareDeploySpec{InstallerType: tc.typ, ContentId: "app-bundle"}
		got, err := applyMockBundle(spec, func(k string) string { return tc.env[k] })
		if got != tc.bundle || (err != nil) != tc.fail {
			t.Fatalf("bundle=%v err=%v", got, err)
		}
		if got && spec.GetExecution().GetMode() != "system" {
			t.Fatal("bundle did not acquire explicit execution mode")
		}
	}
}
