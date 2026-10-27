package softdist

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

type DetectionRule struct {
	Kind, Path, MinimumVersion                           string
	Hive, Key, View, ValueName, ValueType, ExpectedValue string
}
type Detection struct {
	Mode  string
	Rules []DetectionRule
}
type DetectionResult struct {
	Present   bool // ANY selected artifact exists, including a wrong version/value
	Satisfied bool // the ALL/ANY installed requirement is satisfied
}

func (d Detection) PublicFields() []string {
	fields := []string{d.Mode}
	for _, r := range d.Rules {
		fields = append(fields, r.Kind, r.Path, r.MinimumVersion, r.Hive, r.Key, r.View, r.ValueName, r.ValueType, r.ExpectedValue)
	}
	return fields
}

func detectionPath(path string, allowVariables bool) error {
	if path == "" || len(path) > 32767 || strings.ContainsAny(path, "\x00\r\n") || strings.Contains(path, "${package_dir}") {
		return fmt.Errorf("invalid detection path; package_dir is temporary and cannot detect an installed package")
	}
	if allowVariables && strings.Contains(path, "${") {
		return nil
	}
	path = strings.ReplaceAll(path, "/", `\`)
	if strings.Contains(path, "${") || len(path) < 4 || !((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) || path[1:3] != `:\` {
		return fmt.Errorf("detection file path must resolve to an absolute local drive path")
	}
	for _, part := range strings.Split(path[3:], `\`) {
		if part == "" || part == "." || part == ".." || strings.TrimRight(part, " .") != part || strings.ContainsAny(part, `<>:"|?*`) {
			return fmt.Errorf("invalid detection file path component")
		}
		for _, c := range part {
			if c < 32 {
				return fmt.Errorf("invalid detection file path character")
			}
		}
	}
	return nil
}

func fileVersionParts(version string) ([4]uint16, error) {
	var parts [4]uint16
	list := strings.Split(version, ".")
	if len(list) < 1 || len(list) > 4 {
		return parts, fmt.Errorf("file version requires one to four numeric components")
	}
	for i, v := range list {
		if v == "" {
			return parts, fmt.Errorf("invalid file version")
		}
		for _, c := range v {
			if c < '0' || c > '9' {
				return parts, fmt.Errorf("invalid numeric file version")
			}
		}
		n, err := strconv.ParseUint(v, 10, 16)
		if err != nil {
			return parts, fmt.Errorf("file version component out of range")
		}
		parts[i] = uint16(n)
	}
	return parts, nil
}

func (d Detection) Validate() error {
	mode := strings.ToLower(d.Mode)
	if mode != "" && mode != "all" && mode != "any" {
		return fmt.Errorf("detection mode must be all or any")
	}
	if len(d.Rules) < 1 || len(d.Rules) > 32 {
		return fmt.Errorf("detection needs 1-32 rules")
	}
	for i, r := range d.Rules {
		if err := r.validate(); err != nil {
			return fmt.Errorf("detection rule %d: %w", i+1, err)
		}
	}
	return nil
}

func (r DetectionRule) validate() error {
	switch r.Kind {
	case "file_exists", "file_version":
		if r.Hive != "" || r.Key != "" || r.View != "" || r.ValueName != "" || r.ValueType != "" || r.ExpectedValue != "" {
			return fmt.Errorf("file rule must not contain registry settings")
		}
		if err := detectionPath(r.Path, true); err != nil {
			return err
		}
		if r.Kind == "file_version" {
			_, err := fileVersionParts(r.MinimumVersion)
			return err
		}
		if r.MinimumVersion != "" {
			return fmt.Errorf("minimum_version requires file_version rule")
		}
	case "registry_key", "registry_value":
		if r.Path != "" || r.MinimumVersion != "" {
			return fmt.Errorf("registry rule must not contain file settings")
		}
		if r.Hive != "HKLM" && r.Hive != "HKCU" {
			return fmt.Errorf("registry hive must be HKLM or HKCU")
		}
		if r.View != "32" && r.View != "64" {
			return fmt.Errorf("registry view must be explicitly 32 or 64")
		}
		if r.Key == "" || len(r.Key) > 16384 || strings.HasPrefix(r.Key, `\`) || strings.HasSuffix(r.Key, `\`) || strings.ContainsAny(r.Key, "\x00\r\n/") || strings.Contains(r.Key, "${") {
			return fmt.Errorf("registry key must be a literal relative path")
		}
		for _, part := range strings.Split(r.Key, `\`) {
			if part == "" || part == "." || part == ".." {
				return fmt.Errorf("invalid registry key component")
			}
		}
		if strings.ContainsAny(r.ValueName, "\x00\r\n") || len(r.ValueName) > 16383 {
			return fmt.Errorf("invalid registry value name")
		}
		if r.Kind == "registry_key" {
			if r.ValueName != "" || r.ValueType != "" || r.ExpectedValue != "" {
				return fmt.Errorf("registry_key must not contain value settings")
			}
			return nil
		}
		if r.ValueType != "string" && r.ValueType != "dword" && r.ValueType != "qword" {
			return fmt.Errorf("registry value_type must be string, dword or qword")
		}
		if strings.ContainsRune(r.ExpectedValue, 0) || len(r.ExpectedValue) > 65536 {
			return fmt.Errorf("invalid expected registry value")
		}
		if r.ValueType != "string" && !strings.Contains(r.ExpectedValue, "${") {
			bits := 64
			if r.ValueType == "dword" {
				bits = 32
			}
			if _, err := strconv.ParseUint(r.ExpectedValue, 10, bits); err != nil {
				return fmt.Errorf("expected registry integer must be unsigned decimal within type range")
			}
		}
	default:
		return fmt.Errorf("unsupported detection kind")
	}
	return nil
}

// Resolve substitutes only ordinary inputs. Keys/hives/types remain literal so
// variable substitution cannot silently change the detection selector or scope.
func (d Detection) Resolve(params map[string]string) (Detection, error) {
	if err := d.Validate(); err != nil {
		return Detection{}, err
	}
	out := Detection{Mode: d.Mode, Rules: append([]DetectionRule(nil), d.Rules...)}
	for i := range out.Rules {
		r := &out.Rules[i]
		r.Path = Substitute(r.Path, params)
		r.ExpectedValue = Substitute(r.ExpectedValue, params)
		if strings.Contains(r.Path, "${") || strings.Contains(r.ExpectedValue, "${") {
			return Detection{}, fmt.Errorf("unresolved detection variable in rule %d", i+1)
		}
		if r.Kind == "file_exists" || r.Kind == "file_version" {
			if err := detectionPath(r.Path, false); err != nil {
				return Detection{}, fmt.Errorf("detection rule %d: %w", i+1, err)
			}
		}
	}
	return out, out.Validate()
}

func evaluateDetection(ctx context.Context, d Detection, presenceOnly bool, probe func(DetectionRule, bool) (DetectionResult, error)) (DetectionResult, error) {
	if err := d.Validate(); err != nil {
		return DetectionResult{}, err
	}
	result := DetectionResult{Satisfied: strings.ToLower(d.Mode) != "any"}
	for i, r := range d.Rules {
		if err := ctx.Err(); err != nil {
			return DetectionResult{}, err
		}
		item, err := probe(r, presenceOnly)
		if err != nil {
			return DetectionResult{}, fmt.Errorf("detection rule %d: %w", i+1, err)
		}
		result.Present = result.Present || item.Present
		if strings.ToLower(d.Mode) == "any" {
			result.Satisfied = result.Satisfied || item.Satisfied
		} else {
			result.Satisfied = result.Satisfied && item.Satisfied
		}
		// Evaluate every rule: an access/read error must not be hidden by an
		// earlier ANY match or ALL mismatch, especially during uninstall.
	}
	return result, nil
}
