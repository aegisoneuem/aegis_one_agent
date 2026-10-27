package main

import (
	"fmt"
	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"strings"
)

func applyMockBundle(spec *pb.SoftwareDeploySpec, lookup func(string) string) (bool, error) {
	b := &pb.SoftwareBundle{EntryPoint: strings.TrimSpace(lookup("AEGIS_DEPLOY_BUNDLE_ENTRY")), Command: strings.TrimSpace(lookup("AEGIS_DEPLOY_BUNDLE_COMMAND")), Destination: strings.TrimSpace(lookup("AEGIS_DEPLOY_BUNDLE_DEST")), Cleanup: strings.TrimSpace(lookup("AEGIS_DEPLOY_BUNDLE_CLEANUP"))}
	if b.EntryPoint == "" && b.Command == "" && b.Destination == "" && b.Cleanup == "" {
		return false, nil
	}
	o := softdist.BundleOptions{EntryPoint: b.EntryPoint, Command: b.Command, Destination: b.Destination, Cleanup: b.Cleanup, InstallerType: spec.InstallerType}
	if err := o.Validate(); err != nil {
		return false, err
	}
	if spec.FilePath == "" && spec.ContentId == "" {
		return false, fmt.Errorf("bundle needs a ZIP file_path or content_id")
	}
	if a := strings.ReplaceAll(strings.ToLower(spec.HashAlgorithm), "-", ""); a != "" && a != "sha256" {
		return false, fmt.Errorf("bundle hash algorithm must be sha256")
	}
	for key := range spec.Parameters {
		canonical := strings.NewReplacer(".", "_", "-", "_", " ", "_").Replace(strings.TrimSpace(key))
		if strings.EqualFold(canonical, "package_dir") {
			return false, fmt.Errorf("package_dir is reserved")
		}
	}
	p := softdist.Params{FilePath: spec.FilePath, SilentArgs: spec.SilentArgs, Name: spec.Name, Parameters: spec.Parameters, SecureParameters: spec.SecureParameters, PublicMetadata: []string{b.EntryPoint, b.Command, b.Destination, b.Cleanup}}
	if err := softdist.ValidatePrivateInput(p); err != nil {
		return false, err
	}
	if password := spec.GetExecution().GetPasswordUtf8(); len(password) > 0 {
		p.SecureParameters = map[string]string{"RUN_AS_PASSWORD": string(password)}
		if err := softdist.ValidatePrivateInput(p); err != nil {
			return false, fmt.Errorf("account password must not appear in bundle fields")
		}
	}
	spec.Bundle = b
	if spec.Execution == nil {
		spec.Execution = &pb.SoftwareExecution{Mode: "system"}
	}
	return true, nil
}
