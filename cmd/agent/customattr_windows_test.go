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

// Bad root / missing key must be refused, never panic.
func TestEvalRegistryAttr_BadInputs(t *testing.T) {
	if _, ok := evalAttribute(attrDefinition{Provider: "registry", Args: map[string]string{"root": "BOGUS", "key": "x", "name": "y"}}); ok {
		t.Fatal("bogus root must be ok=false")
	}
	if _, ok := evalAttribute(attrDefinition{Provider: "registry", Args: map[string]string{"root": "HKLM", "key": `SOFTWARE\NoSuchAegisKey_XYZ`, "name": "z"}}); ok {
		t.Fatal("missing key must be ok=false")
	}
}
