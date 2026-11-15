package main

import (
	"os"
	"testing"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

func TestSpecToDefs(t *testing.T) {
	spec := &pb.CustomAttributeSpec{
		IssuedAt: 100,
		Definitions: []*pb.CustomAttributeDefinition{
			{Name: "a", Provider: "env", Args: map[string]string{"name": "X"}},
			{Name: "b", Provider: "registry", Args: map[string]string{"root": "HKLM", "key": "K", "name": "N"}},
		},
	}
	defs := specToDefs(spec)
	if len(defs) != 2 || defs[0].Name != "a" || defs[1].Provider != "registry" || defs[1].Args["key"] != "K" {
		t.Fatalf("specToDefs mismatch: %+v", defs)
	}
}

func TestCurrentCustomProperties(t *testing.T) {
	os.Setenv("AEGIS_CA_TEST", "xyz")
	defer os.Unsetenv("AEGIS_CA_TEST")
	customAttrMu.Lock()
	customAttrDefs = []attrDefinition{
		{Name: "temp", Provider: "env", Args: map[string]string{"name": "AEGIS_CA_TEST"}},
		{Name: "missing", Provider: "env", Args: map[string]string{"name": "AEGIS_CA_NOPE_XYZ"}},
	}
	customAttrMu.Unlock()
	defer func() { customAttrMu.Lock(); customAttrDefs = nil; customAttrMu.Unlock() }()

	m := currentCustomProperties()
	if len(m) != 1 || m["temp"] != "xyz" {
		t.Fatalf("currentCustomProperties = %v want only {temp:xyz}", m)
	}
}

func TestCurrentCustomProperties_NilWhenEmpty(t *testing.T) {
	customAttrMu.Lock()
	customAttrDefs = nil
	customAttrMu.Unlock()
	if m := currentCustomProperties(); m != nil {
		t.Fatalf("expected nil with no definitions, got %v", m)
	}
}
