package patch

import "fmt"

// classifyWusaExit maps a wusa.exe exit code onto a DeployResult's outcome
// fields. Pure (no COM/exec) so the code→outcome mapping is unit-tested:
//   0                  → success
//   3010 / 1641        → success + reboot required
//   0x240006           → WU_S_ALREADY_INSTALLED → already installed (NOT_APPLICABLE)
//   0x80240017         → WU_E_NOT_APPLICABLE → not applicable (NOT_APPLICABLE), the
//                        benign "wrong OS/build/arch or missing prerequisite" case
//                        the server's self-correcting diff relies on
//   anything else      → failure
func classifyWusaExit(exitCode int, res *DeployResult) {
	switch exitCode {
	case 0:
		res.Success = true
	case 3010, 1641:
		res.Success = true
		res.RebootRequired = true
	case 0x240006:
		res.AlreadyInstalled = true
		res.Reason = "already installed (nothing to do)"
	case 0x80240017, -2145124329: // WU_E_NOT_APPLICABLE (unsigned + int32 forms)
		res.NotApplicable = true
		res.Reason = "not applicable to this computer (wusa WU_E_NOT_APPLICABLE — wrong OS/build/arch or missing prerequisite)"
	default:
		res.Success = false
		res.Reason = fmt.Sprintf("wusa install failed (exit %d / 0x%X)", exitCode, uint32(exitCode))
	}
}

// DeployResult is the outcome of a local servicing operation (install via DISM, or
// uninstall via wusa). Cross-platform so non-Windows stubs can return it too.
type DeployResult struct {
	Path             string
	Success          bool
	AlreadyInstalled bool // the installer reported the KB is already present (no-op)
	NotApplicable    bool // nothing to do (not installed / superseded) → NOT_APPLICABLE
	RebootRequired   bool
	ExitCode         int
	Reason           string // concise, human failure cause (empty on success)
	Transient        bool   // failure looks transient → safe to retry
	Detail           string // trailing tool output (for logs/console)
}
