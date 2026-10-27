package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/aegisoneuem/aegis_one_agent/internal/deployspec"
	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

func applyMockDetection(spec *pb.SoftwareDeploySpec, lookup func(string) string) (bool, error) {
	count, mode := lookup("AEGIS_DEPLOY_DETECT_COUNT"), lookup("AEGIS_DEPLOY_DETECT_MODE")
	if strings.TrimSpace(count) == "0" {
		spec.Detection = nil // explicit mock-only opt-out for standalone regressions
		return false, nil
	}
	configured := count != "" || mode != ""
	for _, suffix := range []string{"KIND", "PATH", "MIN_VERSION", "HIVE", "KEY", "VIEW", "VALUE_NAME", "VALUE_TYPE", "EXPECTED"} {
		configured = configured || lookup("AEGIS_DEPLOY_DETECT_1_"+suffix) != ""
	}
	if !configured {
		return false, nil
	}
	n, err := strconv.Atoi(count)
	if err != nil || n < 1 || n > 32 {
		return false, fmt.Errorf("AEGIS_DEPLOY_DETECT_COUNT must be 1-32")
	}
	d := &pb.SoftwareDetection{Mode: strings.ToLower(strings.TrimSpace(mode))}
	for i := 1; i <= n; i++ {
		get := func(suffix string) string { return lookup(fmt.Sprintf("AEGIS_DEPLOY_DETECT_%d_%s", i, suffix)) }
		d.Rules = append(d.Rules, &pb.SoftwareDetectionRule{Kind: strings.ToLower(strings.TrimSpace(get("KIND"))), Path: get("PATH"), MinimumVersion: get("MIN_VERSION"), Hive: strings.ToUpper(strings.TrimSpace(get("HIVE"))), Key: get("KEY"), View: get("VIEW"), ValueName: get("VALUE_NAME"), ValueType: strings.ToLower(strings.TrimSpace(get("VALUE_TYPE"))), ExpectedValue: get("EXPECTED")})
	}
	spec.Detection = d
	if err := deployspec.ValidateDetection(spec); err != nil {
		return false, err
	}
	if spec.Execution == nil {
		spec.Execution = &pb.SoftwareExecution{Mode: "system"}
	}
	p := softdist.Params{Parameters: spec.Parameters, SecureParameters: spec.SecureParameters, PublicMetadata: deployspec.Detection(d).PublicFields()}
	if err := softdist.ValidatePrivateInput(p); err != nil {
		return false, err
	}
	if password := spec.GetExecution().GetPasswordUtf8(); len(password) > 0 {
		p.SecureParameters = map[string]string{"RUN_AS_PASSWORD": string(password)}
		if err := softdist.ValidatePrivateInput(p); err != nil {
			return false, fmt.Errorf("account password must not appear in detection rules")
		}
	}
	return true, nil
}
