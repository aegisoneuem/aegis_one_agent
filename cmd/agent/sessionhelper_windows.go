//go:build windows

package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// uiExeName is the distinct process name for the interactive tray helper, so Task
// Manager shows it apart from the SYSTEM service (BigFix shows BESClient.exe vs
// BESClientUI.exe). Single-source-binary: it is a byte-copy of AegisOneClient.exe,
// not a separately built exe.
const uiExeName = "AegisOneClientUI.exe"

// helperExePath returns the path to the shipped tray-UI binary (AegisOneClientUI.exe),
// which the MSI installs next to the service exe. Returns "" if it isn't present (then
// no helper is launched — the service runs fine without the UI). A separate, signed
// binary (not a self-copy) so it carries its own name/description and signature.
func helperExePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	ui := filepath.Join(filepath.Dir(exe), uiExeName)
	if _, err := os.Stat(ui); err != nil {
		return ""
	}
	return ui
}

// Session-helper launcher. The agent runs as LocalSystem in Session 0, which has no
// interactive desktop — it cannot draw a tray icon or a dialog for the logged-in
// user. So the SAME binary is launched, in each active user session, as a short-lived
// helper process ("tray-helper") that runs in the user's desktop and talks back to
// the service over a local IPC (added in a later step). This is the standard UEM
// pattern (BigFix/SCCM) and the helper is shared with Remote-Control capture later.
//
// Single-binary rule: the helper is a MODE of AegisOneClient.exe (subcommand), never
// a separate exe.
//
// Honest bound: launching into a user session needs SeTcbPrivilege — LocalSystem has
// it, so this only works when the agent runs as the service (not in dev/console).

const (
	createUnicodeEnvironment = 0x00000400
	createNoWindow           = 0x08000000
)

// activeSessionIDs returns the WTS sessions that currently have an interactive user
// logged on (state == WTSActive). Session 0 (services) is skipped — it has no user.
func activeSessionIDs() []uint32 {
	var p *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(0 /* WTS_CURRENT_SERVER_HANDLE */, 0, 1, &p, &count); err != nil {
		return nil
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(p)))
	sessions := unsafe.Slice(p, count)
	out := make([]uint32, 0, count)
	for _, s := range sessions {
		if s.State == windows.WTSActive && s.SessionID != 0 {
			out = append(out, s.SessionID)
		}
	}
	return out
}

// launchHelperInSession starts AegisOneClient.exe tray-helper inside sessionID, in the
// logged-on user's security context and on their interactive desktop. Returns the
// process handle so the supervisor can tell when the helper exits.
func launchHelperInSession(sessionID uint32, helperExe string) (windows.Handle, error) {
	var userTok windows.Token
	if err := windows.WTSQueryUserToken(sessionID, &userTok); err != nil {
		return 0, err // no interactive token (e.g. the session just went away)
	}
	defer userTok.Close()

	// Duplicate to a primary token suitable for CreateProcessAsUser.
	var primary windows.Token
	if err := windows.DuplicateTokenEx(userTok, windows.TOKEN_ALL_ACCESS, nil,
		windows.SecurityImpersonation, windows.TokenPrimary, &primary); err != nil {
		return 0, err
	}
	defer primary.Close()

	var env *uint16
	if err := windows.CreateEnvironmentBlock(&env, primary, false); err == nil {
		defer windows.DestroyEnvironmentBlock(env)
	} else {
		env = nil // fall back to no custom environment
	}

	appName, err := windows.UTF16PtrFromString(helperExe)
	if err != nil {
		return 0, err
	}
	// Writable command line buffer (CreateProcess may modify it in place). The UI
	// binary needs no arguments — its main runs the tray directly.
	cmdline := make([]uint16, 0)
	if c, cerr := windows.UTF16FromString(`"` + helperExe + `"`); cerr == nil {
		cmdline = c
	}
	desktop, _ := windows.UTF16PtrFromString(`winsta0\default`)

	si := &windows.StartupInfo{Desktop: desktop}
	si.Cb = uint32(unsafe.Sizeof(*si))
	var pi windows.ProcessInformation

	flags := uint32(createNoWindow)
	if env != nil {
		flags |= createUnicodeEnvironment
	}
	if err := windows.CreateProcessAsUser(primary, appName, &cmdline[0], nil, nil, false,
		flags, env, nil, si, &pi); err != nil {
		return 0, err
	}
	_ = windows.CloseHandle(pi.Thread) // we only track the process
	return pi.Process, nil
}

// runSessionHelperSupervisor keeps exactly one tray-helper alive in each active user
// session. It reconciles every 15s: launch where missing, reap exited helpers (so the
// next tick relaunches), and drop sessions that logged off. Resource-light: a cheap
// WTS enumerate + a few handle checks, no busy loop. Only runs as the service.
func runSessionHelperSupervisor(ctx context.Context) {
	defer recoverLog("session-helper-supervisor")
	if !runningAsService {
		return // dev/console: no SeTcbPrivilege and the user IS session-interactive
	}
	helpers := map[uint32]windows.Handle{} // sessionID -> helper process handle
	defer func() {
		for _, h := range helpers {
			_ = windows.CloseHandle(h)
		}
	}()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	reconcile := func() {
		helperExe := helperExePath() // shipped AegisOneClientUI.exe
		if helperExe == "" {
			return // UI binary not installed — nothing to supervise
		}
		active := activeSessionIDs()
		activeSet := make(map[uint32]bool, len(active))
		for _, s := range active {
			activeSet[s] = true
		}
		// Reap exited helpers and drop logged-off sessions.
		for sid, h := range helpers {
			ev, _ := windows.WaitForSingleObject(h, 0)
			exited := ev == windows.WAIT_OBJECT_0
			if exited || !activeSet[sid] {
				_ = windows.CloseHandle(h)
				delete(helpers, sid)
			}
		}
		// Launch where an active session has no live helper.
		for _, sid := range active {
			if _, ok := helpers[sid]; ok {
				continue
			}
			h, err := launchHelperInSession(sid, helperExe)
			if err != nil {
				slog.Debug("session-helper: launch failed", "session", sid, "error", err.Error())
				continue
			}
			helpers[sid] = h
			slog.Debug("session-helper: launched tray helper", "session", sid)
		}
	}
	reconcile() // don't wait 15s for the first one
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reconcile()
		}
	}
}
