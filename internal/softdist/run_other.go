//go:build !windows

package softdist

import "context"

// Run is a no-op on non-Windows platforms for now — software deploy is Windows
// only in Phase 1. Linux/macOS package deploy will bolt on here later.
func Run(_ context.Context, _ Params) Result {
	return Result{Reason: "software deploy is only supported on Windows"}
}
