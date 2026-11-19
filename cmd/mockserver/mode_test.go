package main

import "testing"

func TestMockModeRequiresExplicitSVSH(t *testing.T) {
	t.Setenv("AEGIS_SVSH", "1")
	t.Setenv("AEGIS_SVSH_SCRIPT", `C:\old\yesterday.svsh`)
	for _, args := range [][]string{nil, {"server"}} {
		mode, err := parseMockMode(args, `C:\old\yesterday.svsh`)
		if err != nil || mode.svsh || mode.script != "" {
			t.Fatalf("normal server opened SVSH: %+v %v", mode, err)
		}
	}
	mode, err := parseMockMode([]string{"svsh"}, `C:\old\yesterday.svsh`)
	if err != nil || !mode.svsh || mode.script != "" {
		t.Fatalf("interactive mode inherited an old script: %+v %v", mode, err)
	}
	mode, err = parseMockMode([]string{"script", `C:\tests\current.svsh`}, `C:\old\yesterday.svsh`)
	if err != nil || mode.script != `C:\tests\current.svsh` {
		t.Fatalf("script path %+v %v", mode, err)
	}
	mode, err = parseMockMode([]string{"script"}, `C:\tests\from-env.svsh`)
	if err != nil || mode.script != `C:\tests\from-env.svsh` {
		t.Fatalf("explicit legacy script %+v %v", mode, err)
	}
	for _, args := range [][]string{{"script"}, {"server", "svsh"}, {"svsh", "extra"}, {"unknown"}} {
		if _, err := parseMockMode(args, ""); err == nil {
			t.Fatalf("accepted invalid mode %v", args)
		}
	}
}
