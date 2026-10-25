package main

import (
	"fmt"
	"strings"
)

const mockModeUsage = `Usage:
  mockserver.exe                      Normal server; logs stay in this window
  mockserver.exe server               Same normal server mode
  mockserver.exe nsh                  Server with interactive NSH in this window
  mockserver.exe script <file.nsh>    Server with one non-interactive NSH job

AEGIS_NSH and AEGIS_NSH_SCRIPT never auto-open NSH in normal server mode.
For "script" only, AEGIS_NSH_SCRIPT can supply the path when no argument is given.
`

type mockMode struct {
	nsh    bool
	script string
	help   bool
}

// Mode is explicit so an inherited NSH environment from yesterday's tests cannot
// hijack the normal server window or redirect SoftDist action statuses to a file.
func parseMockMode(args []string, environmentScript string) (mockMode, error) {
	if len(args) == 0 {
		return mockMode{}, nil
	}
	switch strings.ToLower(args[0]) {
	case "server":
		if len(args) == 1 {
			return mockMode{}, nil
		}
	case "nsh":
		if len(args) == 1 {
			return mockMode{nsh: true}, nil
		}
	case "script":
		if len(args) > 2 {
			break
		}
		path := strings.TrimSpace(environmentScript)
		if len(args) == 2 {
			path = strings.TrimSpace(args[1])
		}
		if path == "" {
			return mockMode{}, fmt.Errorf("script mode needs a .nsh file path")
		}
		return mockMode{nsh: true, script: path}, nil
	case "help", "--help", "-h":
		if len(args) == 1 {
			return mockMode{help: true}, nil
		}
	}
	return mockMode{}, fmt.Errorf("invalid mockserver arguments; use server, nsh, script <file.nsh>, or --help")
}
