package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEvalAttribute_Env(t *testing.T) {
	os.Setenv("AEGIS_TEST_ATTR", "hello")
	defer os.Unsetenv("AEGIS_TEST_ATTR")

	if v, ok := evalAttribute(attrDefinition{Provider: "env", Args: map[string]string{"name": "AEGIS_TEST_ATTR"}}); !ok || v != "hello" {
		t.Fatalf("env attr = %q,%v want hello,true", v, ok)
	}
	if _, ok := evalAttribute(attrDefinition{Provider: "env", Args: map[string]string{"name": "AEGIS_NOPE_XYZ"}}); ok {
		t.Fatal("missing env var must be ok=false")
	}
}

func TestEvalAttribute_File(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, ok := evalAttribute(attrDefinition{Provider: "file", Args: map[string]string{"path": p, "attr": "exists"}}); !ok || v != "true" {
		t.Fatalf("exists = %q,%v want true,true", v, ok)
	}
	if v, ok := evalAttribute(attrDefinition{Provider: "file", Args: map[string]string{"path": filepath.Join(dir, "nope"), "attr": "exists"}}); !ok || v != "false" {
		t.Fatalf("missing exists = %q,%v want false,true", v, ok)
	}
	if v, ok := evalAttribute(attrDefinition{Provider: "file", Args: map[string]string{"path": p, "attr": "size"}}); !ok || v != "5" {
		t.Fatalf("size = %q,%v want 5,true", v, ok)
	}
}

func TestEvalAttribute_UnknownProviderRefused(t *testing.T) {
	if _, ok := evalAttribute(attrDefinition{Provider: "shell", Args: map[string]string{"cmd": "whoami"}}); ok {
		t.Fatal("unknown provider must be refused (ok=false) — no arbitrary exec")
	}
	if _, ok := evalAttribute(attrDefinition{Provider: "", Args: nil}); ok {
		t.Fatal("empty provider must be refused")
	}
}

func TestEvalAttribute_ValueCapped(t *testing.T) {
	big := make([]byte, maxAttrValueLen+500)
	for i := range big {
		big[i] = 'x'
	}
	os.Setenv("AEGIS_TEST_BIG", string(big))
	defer os.Unsetenv("AEGIS_TEST_BIG")
	v, ok := evalAttribute(attrDefinition{Provider: "env", Args: map[string]string{"name": "AEGIS_TEST_BIG"}})
	if !ok || len(v) != maxAttrValueLen {
		t.Fatalf("value len = %d want capped at %d", len(v), maxAttrValueLen)
	}
}

func TestEvalDefinitions_SkipsFailuresAndUnnamed(t *testing.T) {
	os.Setenv("AEGIS_TEST_D", "v")
	defer os.Unsetenv("AEGIS_TEST_D")
	defs := []attrDefinition{
		{Name: "present", Provider: "env", Args: map[string]string{"name": "AEGIS_TEST_D"}},
		{Name: "missing", Provider: "env", Args: map[string]string{"name": "AEGIS_NOPE_XYZ"}},
		{Name: "", Provider: "env", Args: map[string]string{"name": "AEGIS_TEST_D"}}, // unnamed → skipped
		{Name: "bad", Provider: "shell"},                                            // refused
	}
	out := evalDefinitions(defs)
	if len(out) != 1 || out["present"] != "v" {
		t.Fatalf("evalDefinitions = %v want only {present:v}", out)
	}
}
