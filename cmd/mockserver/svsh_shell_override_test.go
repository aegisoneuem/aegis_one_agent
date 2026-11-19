package main

import "testing"

func TestParseShellOverride_HappyPath(t *testing.T) {
	cases := []struct {
		in           string
		wantShell    string
		wantCommand  string
	}{
		// BigFix-style: `/shell`
		{`dir C:\ /shell cmd`, "cmd", `dir C:\`},
		{`Get-Process /shell powershell`, "powershell", "Get-Process"},
		{`ls -la /shell bash`, "bash", "ls -la"},
		// Unix-style: `--shell`
		{`dir C:\ --shell cmd`, "cmd", `dir C:\`},
		{`Get-Process --shell pwsh`, "pwsh", "Get-Process"},
		// Case-insensitive on selector name AND value
		{`dir C:\ /SHELL CMD`, "cmd", `dir C:\`},
		{`dir C:\ --Shell PowerShell`, "powershell", `dir C:\`},
		// Trailing whitespace tolerated
		{`dir C:\ /shell cmd   `, "cmd", `dir C:\`},
	}
	for _, c := range cases {
		gotShell, gotCmd := parseShellOverride(c.in)
		if gotShell != c.wantShell || gotCmd != c.wantCommand {
			t.Errorf("parseShellOverride(%q) = (%q, %q), want (%q, %q)",
				c.in, gotShell, gotCmd, c.wantShell, c.wantCommand)
		}
	}
}

func TestParseShellOverride_NoOverride(t *testing.T) {
	// No trailing selector → return original line + empty shell.
	cases := []string{
		`dir C:\`,
		`Get-Process`,
		`echo /shell`,            // selector without value — not an override
		`echo --shell`,           // selector without value
		`dir /shell potato`,      // invalid shell name — not an override
		`dir --shell C:\temp`,    // invalid shell name in trailing position
		`echo "test"`,
		``,
		`   `,
	}
	for _, in := range cases {
		gotShell, gotCmd := parseShellOverride(in)
		if gotShell != "" || gotCmd != in {
			t.Errorf("parseShellOverride(%q) = (%q, %q), want ('', %q)", in, gotShell, gotCmd, in)
		}
	}
}

// BREAK 1: Operator pastes a PowerShell command that happens to contain
// `/shell` in the middle. Only the TRAILING pattern should match —
// mid-string occurrences must be left alone so the command runs
// unmodified.
func TestParseShellOverride_Break_MidStringSlashShellIgnored(t *testing.T) {
	line := `powershell -Command "Get-Item /shell test.txt"`
	gotShell, gotCmd := parseShellOverride(line)
	if gotShell != "" {
		t.Errorf("mid-string /shell triggered false override: %q", gotShell)
	}
	if gotCmd != line {
		t.Errorf("command text mangled: got %q, want %q", gotCmd, line)
	}
}

// BREAK 2: Only the selector typed, no command (`/shell cmd` alone).
// Must fall back (no override) so the caller surfaces a usage error
// rather than running an empty command.
func TestParseShellOverride_Break_SelectorOnlyNoCommand(t *testing.T) {
	line := `/shell cmd`
	gotShell, gotCmd := parseShellOverride(line)
	if gotShell != "" {
		t.Errorf("/shell cmd with no command returned shell=%q, want '' (fall back for empty command)", gotShell)
	}
	if gotCmd != line {
		t.Errorf("command text mutated on fallback: got %q, want %q", gotCmd, line)
	}
}

// BREAK 3: Double selector — `/shell cmd --shell powershell`. Only the
// trailing one wins; it is the TRUE tail of the line.
func TestParseShellOverride_Break_DoubleSelectorLastWins(t *testing.T) {
	line := `echo hi /shell cmd --shell powershell`
	gotShell, gotCmd := parseShellOverride(line)
	if gotShell != "powershell" {
		t.Errorf("double selector: got shell=%q, want powershell (last wins)", gotShell)
	}
	if gotCmd != `echo hi /shell cmd` {
		t.Errorf("command text: got %q, want %q", gotCmd, `echo hi /shell cmd`)
	}
}

func TestIsValidRunShell(t *testing.T) {
	// MUST match the `use` verb whitelist exactly.
	valid := []string{"cmd", "powershell", "ps", "pwsh", "bash", "sh", "zsh"}
	for _, s := range valid {
		if !isValidRunShell(s) {
			t.Errorf("isValidRunShell(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "CMD", "pwsh.exe", "potato", "python", "node"}
	for _, s := range invalid {
		if isValidRunShell(s) {
			t.Errorf("isValidRunShell(%q) = true, want false", s)
		}
	}
}
