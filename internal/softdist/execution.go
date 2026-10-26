package softdist

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const DefaultMaxSecretBytes = 64 * 1024

// Execution is private job input. Password authenticates the Windows account;
// it is never passed to the installer. DetectionScope is machine or user.
type Execution struct {
	Mode, Username, Domain, DetectionScope string
	Password                               []byte
	SessionID                              *uint32
}

func (e Execution) Validate() error {
	mode := strings.ToLower(strings.TrimSpace(e.Mode))
	if mode == "" {
		mode = "system"
	}
	switch mode {
	case "system":
		if e.Username != "" || e.Domain != "" || len(e.Password) > 0 || e.SessionID != nil {
			return fmt.Errorf("SYSTEM execution must not include account credentials or a session ID")
		}
	case "logged_in_user":
		if e.Username != "" || e.Domain != "" || len(e.Password) > 0 {
			return fmt.Errorf("logged-in-user execution must not include account credentials")
		}
	case "specified_account":
		if strings.TrimSpace(e.Username) == "" || len(e.Password) == 0 || e.SessionID != nil {
			return fmt.Errorf("specified-account execution requires username/password and forbids a session ID")
		}
	default:
		return fmt.Errorf("unknown execution mode")
	}
	scope := strings.ToLower(strings.TrimSpace(e.DetectionScope))
	if scope != "" && scope != "machine" && scope != "user" {
		return fmt.Errorf("detection scope must be machine or user")
	}
	if mode == "system" && scope == "user" {
		return fmt.Errorf("per-user detection requires a user execution context")
	}
	return nil
}

var secretName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// ValidatePrivateInput runs before delivery/logging. Secure values may not be
// substituted into visible command text or copied to ordinary parameter values.
func ValidatePrivateInput(p Params) error {
	limit := p.MaxSecretBytes
	if limit <= 0 {
		limit = DefaultMaxSecretBytes
	}
	total := 0
	seen := map[string]bool{}
	for key, value := range p.SecureParameters {
		if !secretName.MatchString(key) {
			return fmt.Errorf("secure parameter name must use letters, digits and underscores (max 64 characters)")
		}
		canonicalKey := strings.ToUpper(key)
		if seen[canonicalKey] {
			return fmt.Errorf("secure parameter names collide ignoring case")
		}
		seen[canonicalKey] = true
		total += len(key) + len(value)
		for normal := range p.Parameters {
			canonical := strings.NewReplacer(".", "_", "-", "_", " ", "_").Replace(strings.TrimSpace(normal))
			if strings.EqualFold(key, canonical) {
				return fmt.Errorf("secure and ordinary parameter names collide")
			}
		}
		for _, text := range append([]string{p.FilePath, p.SilentArgs, p.Name, p.Version, p.DisplayName, p.UninstallKey, p.ProductCode, p.Action, p.InstallerType, p.ExpectedHash, p.HashAlgorithm}, p.PublicMetadata...) {
			if strings.Contains(text, "${"+key+"}") || (value != "" && strings.Contains(text, value)) {
				return fmt.Errorf("secure values cannot appear in command text, arguments or names; read stdin JSON")
			}
		}
		for normalKey, normal := range p.Parameters {
			if value != "" && strings.Contains(normalKey, value) {
				return fmt.Errorf("secure values cannot be ordinary parameter names")
			}
			if strings.Contains(normal, "${"+key+"}") || (value != "" && strings.Contains(normal, value)) {
				return fmt.Errorf("secure values cannot be copied into ordinary parameters")
			}
		}
	}
	if total > limit {
		return fmt.Errorf("secure parameters exceed the configured byte limit")
	}
	return nil
}

func secretInput(values map[string]string) ([]byte, error) {
	return json.Marshal(struct {
		Version    int               `json:"version"`
		Parameters map[string]string `json:"secure_parameters"`
	}{1, values})
}

// Sensitive jobs suppress installer stdout/stderr and MSI verbose log creation.
// Output cannot reliably be redacted when a vendor encodes/transforms a secret.
func (p Params) sensitive() bool { return len(p.SecureParameters) > 0 || p.Sensitive }
