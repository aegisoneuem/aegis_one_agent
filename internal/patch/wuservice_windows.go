//go:build windows

package patch

import (
	"fmt"
	"log/slog"

	"golang.org/x/sys/windows/svc/mgr"
)

// ensureWUServiceForWUSA makes sure the Windows Update service (wuauserv) is not
// DISABLED before a wusa.exe install — wusa depends on it, which is why BigFix's
// patch Fixlets guard with "continue if wuauserv not disabled". BigFix merely
// REFUSES when it is disabled; we go one better: if wuauserv is disabled we
// temporarily set it to Manual and start it, and the returned restore func
// reverts the start type afterward so a hardened server is left exactly as it
// was. When wuauserv is already enabled this is a no-op (wusa starts it on
// demand). The restore func is always non-nil and safe to defer.
func ensureWUServiceForWUSA() (restore func(), wasDisabled bool) {
	noop := func() {}

	m, err := mgr.Connect()
	if err != nil {
		slog.Warn("deploy: could not connect SCM to check wuauserv before wusa (continuing)", "error", err.Error())
		return noop, false
	}
	s, err := m.OpenService("wuauserv")
	if err != nil {
		m.Disconnect()
		slog.Warn("deploy: could not open wuauserv to check start type (continuing)", "error", err.Error())
		return noop, false
	}
	cfg, cerr := s.Config()
	if cerr != nil {
		s.Close()
		m.Disconnect()
		slog.Warn("deploy: could not read wuauserv config (continuing)", "error", cerr.Error())
		return noop, false
	}
	if cfg.StartType != mgr.StartDisabled {
		s.Close()
		m.Disconnect()
		return noop, false // enabled — wusa will start it on demand
	}

	// wuauserv is DISABLED: temporarily enable (Manual) + start so wusa can run.
	orig := cfg.StartType
	slog.Warn("deploy: wuauserv is DISABLED — temporarily enabling it so the wusa fallback can install; will restore afterward")
	cfg.StartType = mgr.StartManual
	if err := s.UpdateConfig(cfg); err != nil {
		s.Close()
		m.Disconnect()
		slog.Warn("deploy: could not temporarily enable wuauserv; wusa fallback may fail", "error", err.Error())
		return noop, true
	}
	if err := s.Start(); err != nil {
		// May already be starting/running; wusa will wait for it. Not fatal.
		slog.Debug("deploy: wuauserv Start() returned (may already be running)", "error", err.Error())
	}

	restore = func() {
		defer m.Disconnect()
		defer s.Close()
		c, err := s.Config()
		if err != nil {
			slog.Warn("deploy: could not re-read wuauserv config to restore start type", "error", err.Error())
			return
		}
		c.StartType = orig
		if err := s.UpdateConfig(c); err != nil {
			slog.Warn("deploy: could not restore wuauserv start type to Disabled — please verify manually", "error", err.Error())
			return
		}
		slog.Info("deploy: restored wuauserv start type to Disabled after wusa install")
	}
	return restore, true
}

// wuServiceDisabledDetail is a short note added to a deploy Detail when wusa was
// run against a disabled wuauserv (so the console/tester sees why a temporary
// service change happened).
func wuServiceDisabledDetail() string {
	return fmt.Sprintf("(note: wuauserv was disabled and temporarily enabled for the wusa install, then restored)")
}
