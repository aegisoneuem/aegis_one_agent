package main

import "testing"

func TestMockModeRequiresExplicitNSH(t *testing.T) {
	t.Setenv("AEGIS_NSH", "1")
	t.Setenv("AEGIS_NSH_SCRIPT", `C:\old\yesterday.nsh`)
	for _, args := range [][]string{nil, {"server"}} {
		mode, err := parseMockMode(args, `C:\old\yesterday.nsh`)
		if err != nil || mode.nsh || mode.script != "" {
			t.Fatalf("normal server opened NSH: %+v %v", mode, err)
		}
	}
	mode, err := parseMockMode([]string{"nsh"}, `C:\old\yesterday.nsh`)
	if err != nil || !mode.nsh || mode.script != "" {
		t.Fatalf("interactive mode inherited an old script: %+v %v", mode, err)
	}
	mode, err = parseMockMode([]string{"script", `C:\tests\current.nsh`}, `C:\old\yesterday.nsh`)
	if err != nil || mode.script != `C:\tests\current.nsh` {
		t.Fatalf("script path %+v %v", mode, err)
	}
	mode, err = parseMockMode([]string{"script"}, `C:\tests\from-env.nsh`)
	if err != nil || mode.script != `C:\tests\from-env.nsh` {
		t.Fatalf("explicit legacy script %+v %v", mode, err)
	}
	for _, args := range [][]string{{"script"}, {"server", "nsh"}, {"nsh", "extra"}, {"unknown"}} {
		if _, err := parseMockMode(args, ""); err == nil {
			t.Fatalf("accepted invalid mode %v", args)
		}
	}
}
