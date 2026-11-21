//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
)

const (
	systemdUnitName = "aegisone.service"
	systemdUnitPath = "/etc/systemd/system/aegisone.service"
)

// runControlCommand handles install/uninstall/start/stop on Linux via systemd. On
// other non-Windows OSes (macOS/BSD — launchd comes later) it is a no-op.
func runControlCommand(cmd string) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	var err error
	switch cmd {
	case "install":
		err = systemdInstall()
	case "uninstall":
		err = systemdUninstall()
	case "start":
		err = systemctl("start", systemdUnitName)
	case "stop":
		err = systemctl("stop", systemdUnitName)
	default:
		return false
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s failed: %v\n", cmd, err)
		os.Exit(1)
	}
	return true
}

// systemdInstall writes the unit, reloads systemd, and enables+starts the agent. The
// agent runs in the foreground (Type=simple) and stops on SIGTERM, which is exactly
// what systemd drives. Needs root.
func systemdInstall() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.Abs(exe)
	unit := fmt.Sprintf(`[Unit]
Description=Aegis One Endpoint Management Agent
Documentation=https://github.com/aegisoneuem
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=5
User=root
# Hardening (tightened further in the tamper phase):
NoNewPrivileges=true
ProtectSystem=full

[Install]
WantedBy=multi-user.target
`, exe)
	if err := os.WriteFile(systemdUnitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write unit (run as root?): %w", err)
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", "--now", systemdUnitName); err != nil {
		return err
	}
	fmt.Printf("installed + started systemd service %q (%s)\n", systemdUnitName, exe)
	return nil
}

func systemdUninstall() error {
	// Best-effort stop/disable, then remove the unit.
	_ = systemctl("disable", "--now", systemdUnitName)
	if err := os.Remove(systemdUnitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit: %w", err)
	}
	_ = systemctl("daemon-reload")
	fmt.Printf("uninstalled systemd service %q\n", systemdUnitName)
	return nil
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %v: %v: %s", args, err, string(out))
	}
	return nil
}

// refreshServiceStopAcceptance is a no-op on non-Windows (SCM-specific).
func refreshServiceStopAcceptance(selfProtect bool) {}

// runAgentEntry runs in the foreground on non-Windows (systemd Type=simple drives it),
// stopping gracefully on SIGTERM/SIGINT.
func runAgentEntry() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runProcess(ctx)
}
