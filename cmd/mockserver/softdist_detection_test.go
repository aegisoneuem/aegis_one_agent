package main

import (
	"testing"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

func TestMockAdvancedDetectionControlsAndPrivateValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		fail bool
	}{
		{"empty", nil, false},
		{"explicit off clears inherited rules", map[string]string{"AEGIS_DEPLOY_DETECT_COUNT": "0", "AEGIS_DEPLOY_DETECT_1_KIND": "file_exists", "AEGIS_DEPLOY_DETECT_1_PATH": `C:\old\test`}, false},
		{"file", map[string]string{"AEGIS_DEPLOY_DETECT_COUNT": "1", "AEGIS_DEPLOY_DETECT_1_KIND": "file_exists", "AEGIS_DEPLOY_DETECT_1_PATH": `C:\app\done.txt`}, false},
		{"missing count", map[string]string{"AEGIS_DEPLOY_DETECT_1_PATH": `C:\app\done.txt`}, true},
		{"too many", map[string]string{"AEGIS_DEPLOY_DETECT_COUNT": "33"}, true},
		{"secret in path", map[string]string{"AEGIS_DEPLOY_DETECT_COUNT": "1", "AEGIS_DEPLOY_DETECT_1_KIND": "file_exists", "AEGIS_DEPLOY_DETECT_1_PATH": `C:\app\private-value`}, true},
		{"SYSTEM HKCU refused", map[string]string{"AEGIS_DEPLOY_DETECT_COUNT": "1", "AEGIS_DEPLOY_DETECT_1_KIND": "registry_key", "AEGIS_DEPLOY_DETECT_1_HIVE": "HKCU", "AEGIS_DEPLOY_DETECT_1_KEY": `Software\App`, "AEGIS_DEPLOY_DETECT_1_VIEW": "64"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := &pb.SoftwareDeploySpec{SecureParameters: map[string]string{"TOKEN": "private-value"}}
			got, err := applyMockDetection(spec, func(key string) string { return tc.env[key] })
			if (err != nil) != tc.fail {
				t.Fatal(got, err)
			}
			if got && spec.Detection == nil {
				t.Fatal("detection fields lost")
			}
		})
	}
}
