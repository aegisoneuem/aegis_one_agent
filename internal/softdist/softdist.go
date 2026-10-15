// Package softdist executes one server-dispatched software deploy (install or
// uninstall) from a LOCAL installer the server delivered — no internet. It runs
// whatever installer the payload names (msi/exe/ps1/bat): running a PAYLOAD the
// server signed is a deploy action, NOT the agent using PowerShell for its own
// logic (the agent never does that). The agent runs as LocalSystem, so the
// installer inherits SYSTEM and installs silently, machine-wide.
package softdist

import "time"

// Params is the OS-independent input to Run; the platform file turns it into an
// installer process.
type Params struct {
	Name          string        // reporting only ("Google Chrome")
	Action        string        // "install" (default) | "uninstall"
	InstallerType string        // "msi" | "exe" | "ps1" | "bat" | "auto" (infer from extension)
	FilePath      string        // local installer path the server delivered
	SilentArgs    string        // unattended args ("/qn /norestart", "/S", ...)
	ExpectedHash  string        // hex digest; "" skips the integrity check (dev/test only)
	HashAlgorithm string        // "sha256" (default) | "sha512"
	SuccessCodes  []int32       // extra exit codes to treat as success beyond {0,3010,1641}
	Timeout       time.Duration // 0 = package default
	// ProductCode is the MSI ProductCode GUID ("{...}"). When set on an uninstall,
	// Run removes the product by code (msiexec /x {GUID}) with NO installer file —
	// Windows Installer removes it from its own database, the standard-UEM uninstall.
	ProductCode string
	// RequireSignature, when true, makes Run verify the installer's Authenticode
	// signature (trusted publisher) and REFUSE to run an unsigned/untrusted file —
	// the standard-UEM publisher check. Left false for internal/unsigned installers.
	RequireSignature bool
}

// Result is the outcome of one deploy: a terminal mapping (Success /
// NotApplicable / neither = failed) plus the raw exit code and a trimmed output
// tail for the console.
type Result struct {
	ExitCode       int
	Success        bool
	RebootRequired bool
	NotApplicable  bool
	Reason         string // concise status/failure reason (console status column)
	Detail         string // trimmed installer output (troubleshooting)
}
