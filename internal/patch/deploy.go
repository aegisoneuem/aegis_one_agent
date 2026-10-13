package patch

// DeployResult is the outcome of a local servicing operation (install via DISM, or
// uninstall via wusa). Cross-platform so non-Windows stubs can return it too.
type DeployResult struct {
	Path             string
	Success          bool
	AlreadyInstalled bool // the installer reported the KB is already present (no-op)
	RebootRequired   bool
	ExitCode         int
	Reason           string // concise, human failure cause (empty on success)
	Transient        bool   // failure looks transient → safe to retry
	Detail           string // trailing tool output (for logs/console)
}
