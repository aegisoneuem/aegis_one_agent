//go:build windows

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestNativePowerShellScriptUsesCommandContract(t *testing.T) {
	for _, shell := range []string{"powershell", "pwsh"} {
		t.Run(shell, func(t *testing.T) {
			bin, err := exec.LookPath(shell + ".exe")
			if err != nil {
				t.Skip("interpreter not installed")
			}
			s := &nshSession{}
			e := newScriptEngine(s)
			e.vars["TAG"] = "daily-23oct"
			content := e.expand("Write-Host \"tag=${TAG} on $env:COMPUTERNAME\"\n# comment\n\nWrite-Host \"env tag = $env:AEGIS_PARAM_TAG\"\nWrite-Output 'quote \" preserved — नमस्ते'\nexit 7")
			text, picked := buildShellCommand(shell, content)
			if picked != shell {
				t.Fatalf("picked %s", picked)
			}
			// Same -Command contract as resolveShell on the agent.
			cmd := exec.Command(bin, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", text)
			cmd.Env = append(os.Environ(), "AEGIS_PARAM_TAG=daily-23oct")
			out, err := cmd.CombinedOutput()
			if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 7 {
				t.Fatalf("exit=%v, output=%s", err, out)
			}
			for _, want := range []string{"tag=daily-23oct on ", "env tag = daily-23oct", "quote \" preserved"} {
				if !strings.Contains(string(out), want) {
					t.Fatalf("missing %q: %s", want, out)
				}
			}
		})
	}
}
