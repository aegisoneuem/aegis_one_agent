package softdist

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAdvancedDetectionAllAnyAndRemovalPresence(t *testing.T) {
	for _, mode := range []string{"all", "any"} {
		d := Detection{Mode: mode, Rules: []DetectionRule{{Kind: "file_exists", Path: `C:\app\a.txt`}, {Kind: "file_exists", Path: `C:\app\b.txt`}}}
		result, err := evaluateDetection(context.Background(), d, false, func(r DetectionRule, _ bool) (DetectionResult, error) {
			return DetectionResult{Present: r.Path == `C:\app\a.txt`, Satisfied: r.Path == `C:\app\a.txt`}, nil
		})
		if err != nil || !result.Present || result.Satisfied != (mode == "any") {
			t.Fatal(result, err)
		}
		_, err = evaluateDetection(context.Background(), d, false, func(r DetectionRule, _ bool) (DetectionResult, error) {
			if strings.Contains(r.Path, "b.txt") {
				return DetectionResult{}, errors.New("access denied")
			}
			return DetectionResult{Present: true, Satisfied: true}, nil
		})
		if err == nil {
			t.Fatal("ANY/ALL short circuit hid an unreadable artifact")
		}
	}
}

func TestAdvancedDetectionValidationAndVariables(t *testing.T) {
	for _, rule := range []DetectionRule{
		{Kind: "file_exists", Path: `\\server\share\file`},
		{Kind: "file_exists", Path: `C:\app\..\file`},
		{Kind: "file_exists", Path: `${package_dir}\file`},
		{Kind: "file_exists", Path: `C:\file`, Hive: "HKLM"},
		{Kind: "file_version", Path: `C:\file`, MinimumVersion: "1.beta"},
		{Kind: "file_version", Path: `C:\file`, MinimumVersion: "65536"},
		{Kind: "registry_value", Hive: "HKLM", Key: `Software\App`, View: "64", ValueType: "dword", ExpectedValue: "4294967296"},
		{Kind: "registry_key", Hive: "HKLM", Key: `Software\App`, View: ""},
	} {
		if err := (Detection{Rules: []DetectionRule{rule}}).Validate(); err == nil {
			t.Fatalf("invalid rule accepted: %+v", rule)
		}
	}
	d := Detection{Rules: []DetectionRule{{Kind: "file_exists", Path: `${DIR}\app.txt`}, {Kind: "registry_value", Hive: "HKLM", Key: `Software\App`, View: "64", ValueType: "dword", ExpectedValue: "${NUMBER}"}}}
	if _, err := d.Resolve(map[string]string{"DIR": `C:\App`}); err == nil {
		t.Fatal("unknown variable treated as missing state")
	}
	got, err := d.Resolve(map[string]string{"DIR": `C:\App`, "NUMBER": "42"})
	if err != nil || got.Rules[0].Path != `C:\App\app.txt` {
		t.Fatal(got, err)
	}
	if d.Rules[0].Path != `${DIR}\app.txt` {
		t.Fatal("resolve mutated signed definition")
	}
	if _, err := d.Resolve(map[string]string{"DIR": `\\remote\share`, "NUMBER": "42"}); err == nil {
		t.Fatal("variable introduced a remote path")
	}
}
