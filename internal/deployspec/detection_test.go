package deployspec

import (
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"testing"
)

func TestHKCUDetectionBindsOnlyExplicitUserScope(t *testing.T) {
	for _, tc := range []struct {
		mode, scope string
		fail        bool
	}{
		{"system", "machine", true}, {"specified_account", "machine", true}, {"specified_account", "user", false}, {"logged_in_user", "", false}, {"logged_in_user", "machine", true},
	} {
		spec := &pb.SoftwareDeploySpec{Execution: &pb.SoftwareExecution{Mode: tc.mode, DetectionScope: tc.scope}, Detection: &pb.SoftwareDetection{Rules: []*pb.SoftwareDetectionRule{{Kind: "registry_key", Hive: "HKCU", Key: `Software\App`, View: "64"}}}}
		if err := ValidateDetection(spec); (err != nil) != tc.fail {
			t.Fatalf("mode=%s scope=%s error=%v", tc.mode, tc.scope, err)
		}
	}
}
