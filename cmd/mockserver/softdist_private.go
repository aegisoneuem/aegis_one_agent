package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// Only DPAPI ciphertext is persisted. The account password is separate from
// installer secrets and is never provided to the installer.
type deploymentSecrets struct {
	Version    int               `json:"version"`
	Username   string            `json:"username"`
	Domain     string            `json:"domain"`
	Password   []byte            `json:"password_utf8"`
	Parameters map[string]string `json:"secure_parameters"`
}

func (s *deploymentSecrets) clear() { clear(s.Password); clear(s.Parameters) }

func createDeploymentSecrets(path string) error {
	s := deploymentSecrets{Version: 1, Parameters: map[string]string{}}
	defer s.clear()
	var err error
	if s.Username, err = readCredentialLine("Run-As username (blank for no account): ", false); err != nil {
		return err
	}
	if s.Username != "" {
		if s.Domain, err = readCredentialLine("Domain (blank for local/UPN): ", false); err != nil {
			return err
		}
		password, e := readCredentialLine("Account password (hidden): ", true)
		if e != nil {
			return e
		}
		s.Password = []byte(password)
		if len(s.Password) == 0 {
			return fmt.Errorf("account password is required")
		}
	}
	for {
		key, e := readCredentialLine("Secure parameter NAME (blank to finish): ", false)
		if e != nil {
			return e
		}
		if key == "" {
			break
		}
		if _, exists := s.Parameters[key]; exists {
			return fmt.Errorf("duplicate secure parameter name")
		}
		value, e := readCredentialLine("Value (hidden): ", true)
		if e != nil {
			return e
		}
		s.Parameters[key] = value
		if err := softdist.ValidatePrivateInput(softdist.Params{SecureParameters: s.Parameters}); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	defer clear(raw)
	if len(raw) > 128*1024 {
		return fmt.Errorf("credential bundle too large")
	}
	sealed, err := secure.ProtectUserData(raw)
	if err != nil {
		return err
	}
	// Refuse overwriting another file; recreate explicitly when changing secrets.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(sealed)
	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	fmt.Println("Encrypted credential bundle created. Use this same Windows account on this machine to run mockserver.")
	return nil
}

func applyPrivateDeployment(spec *pb.SoftwareDeploySpec, lookup func(string) string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(lookup("AEGIS_DEPLOY_RUN_AS")))
	path := strings.TrimSpace(lookup("AEGIS_DEPLOY_SECRETS_FILE"))
	scope := strings.TrimSpace(lookup("AEGIS_DEPLOY_DETECTION_SCOPE"))
	session := strings.TrimSpace(lookup("AEGIS_DEPLOY_SESSION_ID"))
	if mode == "" && path == "" && scope == "" && session == "" {
		return "software_deploy", nil
	}
	if mode == "" {
		mode = "system"
	}
	e := &pb.SoftwareExecution{Mode: mode, DetectionScope: scope}
	spec.Execution = e
	success := false
	defer func() {
		if !success {
			clear(e.PasswordUtf8)
			clear(spec.SecureParameters)
		}
	}()
	if session != "" {
		id, err := strconv.ParseUint(session, 10, 32)
		if err != nil {
			return "", fmt.Errorf("invalid AEGIS_DEPLOY_SESSION_ID")
		}
		n := uint32(id)
		e.SessionId = &n
	}
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("cannot open encrypted credential bundle")
		}
		info, err := f.Stat()
		if err != nil || info.Size() > 256*1024 {
			f.Close()
			return "", fmt.Errorf("invalid credential bundle size")
		}
		sealed, err := io.ReadAll(io.LimitReader(f, 256*1024+1))
		f.Close()
		if err != nil {
			return "", fmt.Errorf("cannot read credential bundle")
		}
		raw, err := secure.UnprotectUserData(sealed)
		if err != nil {
			return "", fmt.Errorf("cannot decrypt credentials: use the Windows account/machine that created the bundle")
		}
		defer clear(raw)
		var secrets deploymentSecrets
		if err := json.Unmarshal(raw, &secrets); err != nil {
			secrets.clear()
			return "", fmt.Errorf("invalid encrypted credential bundle")
		}
		if secrets.Version != 1 {
			secrets.clear()
			return "", fmt.Errorf("unsupported credential bundle version")
		}
		e.Username, e.Domain, e.PasswordUtf8 = secrets.Username, secrets.Domain, secrets.Password
		spec.SecureParameters = secrets.Parameters
	}
	execution := softdist.Execution{Mode: e.Mode, Username: e.Username, Domain: e.Domain, Password: e.PasswordUtf8, SessionID: e.SessionId, DetectionScope: e.DetectionScope}
	if err := execution.Validate(); err != nil {
		return "", err
	}
	p := softdist.Params{Name: spec.Name, Version: spec.Version, DisplayName: spec.DisplayName, UninstallKey: spec.UninstallKey, ProductCode: spec.ProductCode, FilePath: spec.FilePath, SilentArgs: spec.SilentArgs, Parameters: spec.Parameters, SecureParameters: spec.SecureParameters, PublicMetadata: []string{spec.PackageId, spec.ContentId, spec.InstallerType, spec.Action, spec.ExpectedHash, spec.HashAlgorithm, e.Username, e.Domain}}
	if err := softdist.ValidatePrivateInput(p); err != nil {
		return "", err
	}
	if len(e.PasswordUtf8) > 0 {
		p.SecureParameters = map[string]string{"RUN_AS_PASSWORD": string(e.PasswordUtf8)}
		if softdist.ValidatePrivateInput(p) != nil {
			return "", fmt.Errorf("account password must not appear in public task fields")
		}
	}
	success = true
	return "software_deploy_v2", nil
}
