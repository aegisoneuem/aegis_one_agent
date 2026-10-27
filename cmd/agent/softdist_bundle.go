package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"strings"
)

func bundleOptions(spec *pb.SoftwareDeploySpec) softdist.BundleOptions {
	b := spec.GetBundle()
	return softdist.BundleOptions{EntryPoint: b.GetEntryPoint(), Command: b.GetCommand(), Destination: b.GetDestination(), Cleanup: b.GetCleanup(), InstallerType: spec.InstallerType}
}
func validateBundleDeployment(spec *pb.SoftwareDeploySpec) error {
	if spec.Bundle == nil {
		return fmt.Errorf("software_deploy_bundle requires bundle settings")
	}
	if err := bundleOptions(spec).Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(spec.FilePath) == "" && strings.TrimSpace(spec.ContentId) == "" {
		return fmt.Errorf("bundle needs a ZIP file_path or content_id")
	}
	if a := strings.ReplaceAll(strings.ToLower(spec.HashAlgorithm), "-", ""); a != "" && a != "sha256" {
		return fmt.Errorf("bundle hash_algorithm must be sha256")
	}
	if h, err := hex.DecodeString(spec.ExpectedHash); err != nil || len(h) != sha256.Size {
		return fmt.Errorf("bundle requires a pinned SHA256")
	}
	for name := range spec.Parameters {
		canonical := strings.NewReplacer(".", "_", "-", "_", " ", "_").Replace(strings.TrimSpace(name))
		if strings.EqualFold(canonical, "package_dir") {
			return fmt.Errorf("package_dir is reserved for the verified bundle directory")
		}
	}
	return nil
}
