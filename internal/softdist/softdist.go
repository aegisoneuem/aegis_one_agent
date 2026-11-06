// Package softdist executes one server-dispatched software deploy (install or
// uninstall) from a LOCAL installer the server delivered — no internet. It runs
// whatever installer the payload names (msi/exe/ps1/bat): running a PAYLOAD the
// server signed is a deploy action, NOT the agent using PowerShell for its own
// logic (the agent never does that). The agent runs as LocalSystem, so the
// installer defaults to SYSTEM; explicit sessions bind user-context actions.
package softdist

import "time"

// Params is the OS-independent input to Run; the platform file turns it into an
// installer process.
type Params struct {
	Name             string        // reporting only ("Google Chrome")
	Action           string        // "install" (default) | "uninstall"
	InstallerType    string        // "msi" | "exe" | "ps1" | "bat" | "auto" (infer from extension)
	FilePath         string        // local installer path the server delivered
	WorkingDirectory string        // explicit verified package root; overrides user-profile cwd
	SilentArgs       string        // unattended args ("/qn /norestart", "/S", ...)
	ExpectedHash     string        // required pinned digest for file-based payloads
	HashAlgorithm    string        // "sha256" (default) | "sha512"
	SuccessCodes     []int32       // extra exit codes to treat as success beyond {0,3010,1641}
	Timeout          time.Duration // 0 = package default
	// ProductCode is the MSI ProductCode GUID ("{...}"). When set on an uninstall,
	// Run removes the product by code (msiexec /x {GUID}) with NO installer file —
	// Windows Installer removes it from its own database, the standard-UEM uninstall.
	ProductCode string
	// UninstallKey / DisplayName locate a non-MSI (EXE/script-installed) product in the
	// Windows registry Uninstall hive. When set on an uninstall, Run reads the product's
	// QuietUninstallString (vendor-silent, preferred) or UninstallString + SilentArgs
	// and runs it — no installer file needed. UninstallKey is the exact subkey name,
	// optionally prefixed HKLM32\ or HKLM64\. DisplayName prefers exact matches then
	// a unique case-insensitive substring. Ambiguity/read errors fail the action.
	UninstallKey string
	DisplayName  string
	// Version restricts registry uninstalls to an exact DisplayVersion. Install
	// minimum-version handling remains in the agent's delivery/verification layer.
	Version string
	// Parameters are name→value pairs (built-in runtime vars + author-supplied)
	// substituted as ${name} into SilentArgs and the "command" invocation, and also
	// exported to child scripts as AEGIS_PARAM_<NAME> environment variables.
	Parameters       map[string]string
	PublicMetadata   []string          // additional visible task fields checked for accidental secret copies
	SecureParameters map[string]string // child stdin JSON only; never argv/env/logs
	MaxSecretBytes   int
	Sensitive        bool     // account credential jobs also suppress diagnostic payload output
	Session          *Session // opened once by worker, shared by detection/execution/verification
	// RequireSignature, when true, makes Run verify the installer's Authenticode
	// signature (trusted publisher) and REFUSE to run an unsigned/untrusted file —
	// the standard-UEM publisher check. Left false for internal/unsigned installers.
	RequireSignature bool
	// RequireACPower, when true, REFUSES the deploy if the machine is running on
	// battery (not on AC/mains). The safety gate for a BIOS/firmware flash — a
	// power loss mid-flash can brick the device. Deeper flash safety (BitLocker
	// suspend, multi-reboot sequencing) is owned by the vendor flash utility.
	RequireACPower bool
}

// Result is the outcome of one deploy: a terminal mapping (Success /
// NotApplicable / neither = failed) plus the raw exit code and a trimmed output
// tail for the console.
type Result struct {
	PackageDirectory string
	PackageCleanup   string
	ExitCode         int
	Success          bool
	RebootRequired   bool
	NotApplicable    bool
	Reason           string // concise status/failure reason (console status column)
	Detail           string // trimmed installer output (troubleshooting)
}
