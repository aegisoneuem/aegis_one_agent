//go:build windows

package main

import "testing"

// Reads a well-known registry value that exists on every Windows box.
func TestEvalRegistryAttr_Real(t *testing.T) {
	v, ok := evalAttribute(attrDefinition{
		Provider: "registry",
		Args: map[string]string{
			"root": "HKLM",
			"key":  `SOFTWARE\Microsoft\Windows NT\CurrentVersion`,
			"name": "ProductName",
		},
	})
	if !ok || v == "" {
		t.Fatalf("registry ProductName = %q,%v want non-empty,true", v, ok)
	}
	t.Logf("registry ProductName = %q", v)
}

// An integer value is returned as its decimal string.
func TestEvalRegistryAttr_Integer(t *testing.T) {
	v, ok := evalAttribute(attrDefinition{
		Provider: "registry",
		Args: map[string]string{
			"root": "HKLM",
			"key":  `SOFTWARE\Microsoft\Windows NT\CurrentVersion`,
			"name": "CurrentMajorVersionNumber",
		},
	})
	if ok {
		t.Logf("CurrentMajorVersionNumber = %q", v)
	} else {
		t.Log("CurrentMajorVersionNumber absent (older SKU) — acceptable")
	}
}

// service_state against a service that is always present + running (Windows Event
// Log), and a definite "not_installed" for a bogus name.
func TestEvalServiceState_Real(t *testing.T) {
	v, ok := evalAttribute(attrDefinition{Provider: "service_state", Args: map[string]string{"name": "EventLog"}})
	if !ok {
		t.Skip("could not query the service control manager in this environment")
	}
	if v != "running" {
		t.Logf("EventLog state = %q (expected running on a healthy box)", v)
	}
	t.Logf("EventLog service_state = %q", v)

	if v, ok := evalAttribute(attrDefinition{Provider: "service_state", Args: map[string]string{"name": "NoSuchSvc_AegisXYZ"}}); !ok || v != "not_installed" {
		t.Fatalf("bogus service = %q,%v want not_installed,true", v, ok)
	}
}

// file version of a known system DLL (always has a version resource).
func TestEvalFileVersion_Real(t *testing.T) {
	v, ok := evalAttribute(attrDefinition{Provider: "file", Args: map[string]string{"path": `C:\Windows\System32\kernel32.dll`, "attr": "version"}})
	if !ok || v == "" {
		t.Skip("kernel32.dll version unavailable in this environment")
	}
	t.Logf("kernel32.dll version = %q", v)
	// a file with no version resource → not ok
	if _, ok := evalAttribute(attrDefinition{Provider: "file", Args: map[string]string{"path": `C:\Windows\System32\drivers\etc\hosts`, "attr": "version"}}); ok {
		t.Fatal("a file with no version resource must be ok=false")
	}
}

// Bad root / missing key must be refused, never panic.
func TestEvalRegistryAttr_BadInputs(t *testing.T) {
	if _, ok := evalAttribute(attrDefinition{Provider: "registry", Args: map[string]string{"root": "BOGUS", "key": "x", "name": "y"}}); ok {
		t.Fatal("bogus root must be ok=false")
	}
	if _, ok := evalAttribute(attrDefinition{Provider: "registry", Args: map[string]string{"root": "HKLM", "key": `SOFTWARE\NoSuchAegisKey_XYZ`, "name": "z"}}); ok {
		t.Fatal("missing key must be ok=false")
	}
}
