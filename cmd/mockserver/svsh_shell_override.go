package main

import "strings"

// parseShellOverride extracts a trailing "/shell <X>" or "--shell <X>"
// per-command shell selector from an SVSH run/exec/fanout command line.
// Returns the overridden shell name (lowercased) and the command text
// with the selector stripped. When no valid trailing selector is found,
// returns ("", original) — the caller then uses the session default.
//
// Design rules (kept tight so command text with slashes is not misread):
//   - The token MUST be the LAST whitespace-separated field in the line
//     (common case: `run <cmd> /shell cmd`).
//   - Valid shell names are the exact set the `use` verb accepts.
//   - Case-insensitive match on the selector name AND the shell value.
//   - Unknown shell value → treat the whole thing as command text (no
//     surprise override, operator gets the session default and a clear
//     path to fix via `use`).
func parseShellOverride(line string) (shell, command string) {
	trimmed := strings.TrimRight(line, " \t")
	if trimmed == "" {
		return "", line
	}
	// Last whitespace-separated field: the shell VALUE.
	iSpace := strings.LastIndexAny(trimmed, " \t")
	if iSpace < 0 {
		return "", line
	}
	shellVal := strings.ToLower(strings.TrimSpace(trimmed[iSpace+1:]))
	if !isValidRunShell(shellVal) {
		return "", line
	}
	// The token BEFORE the shell value must be /shell or --shell.
	head := strings.TrimRight(trimmed[:iSpace], " \t")
	jSpace := strings.LastIndexAny(head, " \t")
	if jSpace < 0 {
		return "", line
	}
	selector := strings.ToLower(strings.TrimSpace(head[jSpace+1:]))
	if selector != "/shell" && selector != "--shell" {
		return "", line
	}
	remaining := strings.TrimRight(head[:jSpace], " \t")
	if remaining == "" {
		// Operator typed `run /shell cmd` with no command — fall back.
		return "", line
	}
	return shellVal, remaining
}

// isValidRunShell mirrors the whitelist in verbUse (set via `use <X>`).
// Keeping the two lists in sync is critical — tests pin the overlap.
func isValidRunShell(s string) bool {
	switch s {
	case "cmd", "powershell", "ps", "pwsh", "bash", "sh", "zsh":
		return true
	}
	return false
}
