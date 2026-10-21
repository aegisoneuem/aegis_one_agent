//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// runOneCommand picks the interpreter, launches the command, and streams its
// stdout+stderr into (a) an inline capture capped at maxOut bytes per stream
// (goes in ActionStatus.Detail), and (b) a FULL on-disk stdout temp when the
// caller intends to upload it. OS-native shells only; an unsupported shell for
// this OS is reported honestly as NotApplicable (NOT silently coerced).
func runOneCommand(ctx context.Context, text, shell, cwd string, params map[string]string, maxOut int64) runCommandOutput {
	bin, args, picked, err := resolveShell(shell, text)
	if err != nil {
		return runCommandOutput{ShellUsed: shell, NotApplicable: true, Reason: err.Error()}
	}

	// Full-stdout temp (always created; the worker uses it only when upload_as
	// was set, else it is removed with the temp dir cleanup).
	tmp, terr := os.CreateTemp("", "aegis-nsh-stdout-*.out")
	if terr != nil {
		return runCommandOutput{ShellUsed: picked, Reason: "create stdout temp: " + terr.Error()}
	}
	tmpPath := tmp.Name()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	// Author/author-forwarded params go into the child env as AEGIS_PARAM_<name>
	// so scripts can read them (same convention as softdist).
	if len(params) > 0 {
		cmd.Env = append(os.Environ(), paramEnvList(params)...)
	}
	stdoutR, _ := cmd.StdoutPipe()
	stderrR, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return runCommandOutput{ShellUsed: picked, Reason: "start: " + err.Error()}
	}

	// Pump stdout to BOTH an inline capped buffer AND the full temp file so
	// upload_as can carry the full output. stderr is inline-only (caps apply).
	var (
		stdoutInline strings.Builder
		stderrInline strings.Builder
		stdoutTotal  int64
		stderrTotal  int64
		truncated    bool
		wg           sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, rerr := stdoutR.Read(buf)
			if n > 0 {
				stdoutTotal += int64(n)
				if _, werr := tmp.Write(buf[:n]); werr != nil {
					slogOnce("run_command: stdout temp write", werr)
				}
				if int64(stdoutInline.Len()) < maxOut {
					room := maxOut - int64(stdoutInline.Len())
					if int64(n) <= room {
						stdoutInline.Write(buf[:n])
					} else {
						stdoutInline.Write(buf[:room])
						truncated = true
					}
				} else {
					truncated = true
				}
			}
			if rerr != nil {
				if !errors.Is(rerr, io.EOF) && ctx.Err() == nil {
					slogOnce("run_command: stdout read", rerr)
				}
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		buf := make([]byte, 16*1024)
		for {
			n, rerr := stderrR.Read(buf)
			if n > 0 {
				stderrTotal += int64(n)
				if int64(stderrInline.Len()) < maxOut {
					room := maxOut - int64(stderrInline.Len())
					if int64(n) <= room {
						stderrInline.Write(buf[:n])
					} else {
						stderrInline.Write(buf[:room])
						truncated = true
					}
				}
			}
			if rerr != nil {
				return
			}
		}
	}()
	wg.Wait()
	werr := cmd.Wait()
	tmp.Close()

	exit := 0
	reason := ""
	if werr != nil {
		if ee, ok := werr.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else if ctx.Err() == context.DeadlineExceeded {
			exit, reason = -1, "timeout"
		} else {
			exit = -1
			reason = werr.Error()
		}
	}
	return runCommandOutput{
		ExitCode:       exit,
		ShellUsed:      picked,
		Stdout:         stdoutInline.String(),
		Stderr:         stderrInline.String(),
		StdoutBytes:    stdoutTotal,
		StderrBytes:    stderrTotal,
		Truncated:      truncated,
		FullStdoutPath: tmpPath,
		Reason:         reason,
	}
}

// resolveShell picks the interpreter + arg list for one command text. "auto" or
// empty defaults to cmd on Windows. Honor the admin's choice — we never guess
// silent flags or wrap text differently once a shell is named.
func resolveShell(shell, text string) (bin string, args []string, picked string, err error) {
	switch strings.ToLower(strings.TrimSpace(shell)) {
	case "", "auto", "cmd":
		return "cmd.exe", []string{"/C", text}, "cmd", nil
	case "powershell", "ps", "ps1":
		return "powershell.exe", []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", text}, "powershell", nil
	case "pwsh":
		if _, err := exec.LookPath("pwsh.exe"); err != nil {
			return "", nil, "pwsh", fmt.Errorf("pwsh.exe not installed on this endpoint")
		}
		return "pwsh.exe", []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", text}, "pwsh", nil
	case "bash", "sh", "zsh":
		return "", nil, shell, fmt.Errorf("%q is a POSIX shell; not available on Windows endpoint", shell)
	default:
		return "", nil, shell, fmt.Errorf("unknown shell %q", shell)
	}
}

// paramEnvList turns params into AEGIS_PARAM_<NAME>=value strings for the
// child process environment (same convention softdist uses).
func paramEnvList(p map[string]string) []string {
	out := make([]string, 0, len(p))
	for k, v := range p {
		if k = strings.TrimSpace(k); k == "" {
			continue
		}
		out = append(out, "AEGIS_PARAM_"+strings.ToUpper(k)+"="+v)
	}
	return out
}

// slogOnce is a tiny helper used by the pump goroutines to avoid pulling slog
// into every error path — real errors still surface via Reason/exit code.
func slogOnce(_ string, _ error) {}

// Compile-time hint: filepath is used by other platform files; keep the import
// stable so a shared helper that moves here does not break the build.
var _ = filepath.Join
