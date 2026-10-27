package main

import (
	"fmt"
	"strings"

	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

func privateDeployment(spec *pb.SoftwareDeploySpec) bool {
	return spec.Execution != nil || len(spec.SecureParameters) > 0
}

func deploymentExecution(spec *pb.SoftwareDeploySpec) softdist.Execution {
	e := spec.GetExecution()
	if e == nil {
		return softdist.Execution{Mode: "system"}
	}
	return softdist.Execution{Mode: e.Mode, Username: e.Username, Domain: e.Domain, Password: e.PasswordUtf8, SessionID: e.SessionId, DetectionScope: e.DetectionScope}
}

func validatePrivateDeployment(spec *pb.SoftwareDeploySpec, limit int) error {
	if err := deploymentExecution(spec).Validate(); err != nil {
		return err
	}
	if limit <= 0 {
		limit = softdist.DefaultMaxSecretBytes
	}
	count := len(spec.GetExecution().GetPasswordUtf8())
	for key, value := range spec.SecureParameters {
		count += len(key) + len(value)
	}
	if count > limit {
		return fmt.Errorf("private deployment input exceeds the configured byte limit")
	}
	p := softdist.Params{PublicMetadata: []string{spec.PackageId, spec.ContentId, spec.InstallerType, spec.Action, spec.ExpectedHash, spec.HashAlgorithm, spec.GetExecution().GetUsername(), spec.GetExecution().GetDomain()}, Name: spec.Name, Version: spec.Version, DisplayName: spec.DisplayName, UninstallKey: spec.UninstallKey, ProductCode: spec.ProductCode, FilePath: spec.FilePath, SilentArgs: spec.SilentArgs, Parameters: spec.Parameters, SecureParameters: spec.SecureParameters, MaxSecretBytes: limit}
	if b := spec.GetBundle(); b != nil {
		p.PublicMetadata = append(p.PublicMetadata, b.EntryPoint, b.Command, b.Destination, b.Cleanup)
	}
	if err := softdist.ValidatePrivateInput(p); err != nil {
		return err
	}
	if password := spec.GetExecution().GetPasswordUtf8(); len(password) > 0 {
		p.SecureParameters = map[string]string{"RUN_AS_PASSWORD": string(password)}
		if err := softdist.ValidatePrivateInput(p); err != nil {
			return fmt.Errorf("account password must not appear in public parameters or command text")
		}
	}
	return nil
}

func scopedDetectProduct(spec *pb.SoftwareDeploySpec, session *softdist.Session) (bool, string, error) {
	if strings.TrimSpace(spec.ProductCode) != "" {
		return softdist.QueryProductInScope(strings.TrimSpace(spec.ProductCode), session.DetectionSID())
	}
	if strings.TrimSpace(spec.UninstallKey) == "" && strings.TrimSpace(spec.DisplayName) == "" {
		return false, "", nil
	}
	selector := softdist.AppSelector{UninstallKey: spec.UninstallKey, DisplayName: spec.DisplayName, UserSID: session.DetectionSID()}
	if strings.EqualFold(strings.TrimSpace(spec.Action), "uninstall") {
		selector.Version = spec.Version
	}
	app, err := softdist.ResolveInstalledApp(selector)
	if err != nil {
		return false, "", err
	}
	if app == nil {
		return false, "", nil
	}
	return true, app.DisplayVersion, nil
}

// Called on the worker after authentication/profile loading, never the stream
// receiver. The same token-derived SID is retained for the post-action check.
func scopedDeploymentPrecheck(spec *pb.SoftwareDeploySpec, session *softdist.Session) (bool, string, error) {
	if spec.ProductCode == "" && spec.UninstallKey == "" && spec.DisplayName == "" {
		return false, "", nil
	}
	present, version, err := scopedDetectProduct(spec, session)
	if err != nil {
		return false, "", err
	}
	uninstall := strings.EqualFold(strings.TrimSpace(spec.Action), "uninstall")
	if uninstall && !present {
		return true, "not installed in the selected execution scope (nothing to uninstall)", nil
	}
	if !uninstall && present && (spec.Version == "" || (version != "" && softdist.CompareVersions(version, spec.Version) >= 0)) {
		return true, "already installed in the selected execution scope (skipped)", nil
	}
	if uninstall && present && spec.ProductCode == "" {
		app, err := softdist.ResolveInstalledApp(softdist.AppSelector{UninstallKey: spec.UninstallKey, DisplayName: spec.DisplayName, Version: spec.Version, UserSID: session.DetectionSID()})
		if err != nil {
			return false, "", err
		}
		if app == nil {
			return false, "", fmt.Errorf("selected registration disappeared during precheck; retry the task")
		}
		// This spec belongs exclusively to the worker. Do not clone password bytes.
		spec.UninstallKey = app.RegistryView + `\` + app.Key
		spec.DisplayName = ""
	}
	return false, "", nil
}

func clearPrivateDeployment(spec *pb.SoftwareDeploySpec) {
	if spec == nil {
		return
	}
	clear(spec.GetExecution().GetPasswordUtf8())
	clear(spec.SecureParameters)
}
