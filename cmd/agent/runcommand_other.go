//go:build !windows

package main

import (
	"context"
	"fmt"
)

// runOneCommand stub for non-Windows builds (Linux CI). The agent only runs on
// Windows in Phase 1; this exists so the whole package still compiles on
// linux/amd64 for vet and tests. The real POSIX runner (bash/sh/zsh) will
// land here when the agent targets Linux/macOS — same proto, same handler,
// only this runner swaps.
func runOneCommand(_ context.Context, _, shell, _ string, _ map[string]string, _ int64) runCommandOutput {
	return runCommandOutput{
		ShellUsed:     shell,
		NotApplicable: true,
		Reason:        fmt.Sprintf("run_command is Windows-only in this build (shell=%q)", shell),
	}
}
