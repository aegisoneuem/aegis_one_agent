// Package deployspec shares portable deployment schema validation between the
// agent, reference server and server-side dependency runner.
package deployspec

import (
	"fmt"
	"strings"

	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

func Detection(d *pb.SoftwareDetection) softdist.Detection {
	out := softdist.Detection{Mode: d.GetMode()}
	for _, r := range d.GetRules() {
		out.Rules = append(out.Rules, softdist.DetectionRule{Kind: r.GetKind(), Path: r.GetPath(), MinimumVersion: r.GetMinimumVersion(), Hive: r.GetHive(), Key: r.GetKey(), View: r.GetView(), ValueName: r.GetValueName(), ValueType: r.GetValueType(), ExpectedValue: r.GetExpectedValue()})
	}
	return out
}

func ValidateDetection(spec *pb.SoftwareDeploySpec) error {
	if spec.Detection == nil {
		return fmt.Errorf("software_deploy_detect requires detection settings")
	}
	if spec.ProductCode != "" || spec.UninstallKey != "" || spec.DisplayName != "" || spec.Version != "" {
		return fmt.Errorf("advanced detection must not mix legacy product selectors/version; put minimum_version on file_version rules")
	}
	d := Detection(spec.Detection)
	if err := d.Validate(); err != nil {
		return err
	}
	for _, r := range d.Rules {
		if r.Hive == "HKCU" {
			e := spec.GetExecution()
			mode := strings.ToLower(strings.TrimSpace(e.GetMode()))
			scope := strings.ToLower(strings.TrimSpace(e.GetDetectionScope()))
			if (mode != "logged_in_user" && mode != "specified_account") || (scope != "user" && !(scope == "" && mode == "logged_in_user")) {
				return fmt.Errorf("HKCU rule requires selected user execution and user detection scope")
			}
		}
	}
	return nil
}
