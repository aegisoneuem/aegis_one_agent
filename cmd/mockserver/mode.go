package main

import (
	"fmt"
	"strings"
)

const mockModeUsage = `Usage:
  mockserver.exe pack <source-folder> <new-bundle.zip>  Create a manifest/hash ZIP bundle
  mockserver.exe secrets <new-file.dpapi>              Enter encrypted credentials
  mockserver.exe                      Normal server; logs stay in this window
  mockserver.exe server               Same normal server mode
  mockserver.exe nsh                  Server with interactive NSH in this window
  mockserver.exe script <file.nsh>    Server with one non-interactive NSH job

Bundle workflow (software_deploy_workflow):
  AEGIS_DEPLOY_PRE_COMMAND / PRE_ARGS / PRE_NAME
  AEGIS_DEPLOY_POST_COMMAND / POST_ARGS / POST_NAME
  Optional per stage: PRE_TIMEOUT_SECONDS, PRE_SUCCESS_CODES, PRE_SECURE_INPUT
  (or POST_*). Requires bundle settings; see docs/softdist-today-testing.md#b-prepost-commands.

Package dependency (server-driven plan):
  Main: AEGIS_DEPLOY_*; prerequisite: AEGIS_DEPENDENCY_ID/TYPE/FILE/ARGS
  Dependency requires PRODUCT_CODE or UNINSTALL_KEY/DISPLAY_NAME detection.
  AEGIS_DEPENDENCY_SERVE_FILE supplies its catalog payload separately.
  See docs/softdist-today-testing.md#c-package-dependencies for three manual cases.

Advanced detection:
  AEGIS_DEPLOY_DETECT_COUNT=1..32; DETECT_MODE=all|any
  AEGIS_DEPLOY_DETECT_1_KIND/PATH/MIN_VERSION or HIVE/KEY/VIEW/VALUE_NAME/VALUE_TYPE/EXPECTED
  See docs/softdist-today-testing.md#d-advanced-detection. Replace BOTH agent and mockserver.

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
