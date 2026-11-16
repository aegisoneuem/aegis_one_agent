//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/aegisoneuem/aegis_one_agent/internal/config"
)

const (
	serviceName    = "AegisOneClient"
	serviceDisplay = "AegisOne Client"
	serviceDesc    = "Aegis One endpoint management agent — patch detection and remediation."
)

// runAgentEntry runs as a Windows service when launched by the SCM, otherwise in
// the console (dev), stopping gracefully on Ctrl+C.
// serviceSelfProtect is read once before the service control loop starts. When
// true (AEGIS_TAMPER_PROTECT), the service does NOT accept the SCM STOP control,
// so `sc stop` / services.msc fail — the only legit stop is a token-authorized
// stop_agent command.
var serviceSelfProtect bool

// Published status channel so a runtime passcode set/clear can re-report whether the
// service accepts SCM stop — without waiting for a restart.
var (
	svcStatusMu sync.Mutex
	svcStatusCh chan<- svc.Status
	svcAccepted svc.Accepted
)

// refreshServiceStopAcceptance re-reports the service's accepted controls to the SCM
// so that setting a passcode immediately makes `sc stop` fail, and clearing it
// immediately makes `sc stop` work again — no restart needed.
func refreshServiceStopAcceptance(selfProtect bool) {
	svcStatusMu.Lock()
	defer svcStatusMu.Unlock()
	if svcStatusCh == nil {
		return // console mode, or not yet running as a service
	}
	accepted := svc.AcceptShutdown
	if !selfProtect {
		accepted |= svc.AcceptStop
	}
	if accepted == svcAccepted {
		return
	}
	svcAccepted = accepted
	select {
	case svcStatusCh <- svc.Status{State: svc.Running, Accepts: accepted}:
		serviceSelfProtect = selfProtect
	case <-time.After(2 * time.Second): // never wedge the caller if the SCM isn't reading
	}
}

func runAgentEntry() {
	if isSvc, err := svc.IsWindowsService(); err == nil && isSvc {
		runningAsService = true
		cfg := config.Load()
		// Self-protect (refuse SCM stop) when the master switch is on, an unprotect
		// passcode is set, OR the provisioned marker is present (fail-closed: a deleted
		// passcode file does NOT drop protection).
		serviceSelfProtect = cfg.TamperProtect || passcodeStoreExists(cfg.SecureDir) || readUnprotectMarker()
		if err := svc.Run(serviceName, &agentService{}); err != nil {
			os.Exit(1) // no console to print to
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runProcess(ctx)
}

// agentService adapts runProcess to the Windows service control protocol.
type agentService struct{}

func (a *agentService) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	// Self-protected: accept SHUTDOWN (clean OS shutdown) but NOT STOP, so an admin's
	// `sc stop` / services.msc stop fails — the only stop is a token-authorized
	// stop_agent command (which cancels the context directly, not via the SCM).
	accepted := svc.AcceptShutdown
	if !serviceSelfProtect {
		accepted |= svc.AcceptStop
	}
	// Publish the status channel so a runtime set/clear of the unprotect passcode can
	// immediately re-report stop-acceptance (no restart needed).
	svcStatusMu.Lock()
	svcStatusCh = s
	svcAccepted = accepted
	svcStatusMu.Unlock()
	s <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // ensure release on every exit path (vet: no context leak)
	done := make(chan struct{})
	go func() {
		runProcess(ctx)
		close(done)
	}()

	s <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-done:
				case <-time.After(20 * time.Second): // bounded graceful wait
				}
				s <- svc.Status{State: svc.Stopped}
				return false, 0
			}
		case <-done:
			// runProcess returned on its own; report stopped.
			s <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}

// runControlCommand handles install/uninstall/start/stop (returns true if it
// handled the arg). Errors print and exit non-zero.
func runControlCommand(cmd string) bool {
	var err error
	switch cmd {
	case "install":
		fips := false
		for _, a := range os.Args[2:] {
			if a == "--fips" || a == "-fips" || a == "/fips" {
				fips = true
			}
		}
		err = installService(fips)
	case "uninstall":
		err = uninstallService()
	case "start":
		err = startService()
	case "stop":
		// `stop --token <T>` uses the token-gated local control endpoint (works on a
		// self-protected service); plain `stop` uses the SCM (works only when not
		// self-protected).
		if token, wantToken := stopTokenFromArgs(os.Args[2:]); wantToken {
			err = tokenStop(token)
		} else {
			err = stopService()
		}
	case "uninstall-check":
		// MSI uninstall gate: exit 0 = allow, non-zero = abort the uninstall.
		err = uninstallCheck(os.Args[2:])
	default:
		return false
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s failed: %v\n", cmd, err)
		os.Exit(1)
	}
	return true
}

// installService installs the LocalSystem auto-start service. When fips is true
// it writes GODEBUG=fips140=on into the service's own environment block so the
// service starts with the Go FIPS 140-3 module active — a single binary toggled
// by an install-time parameter (no separate FIPS binary), BigFix-style.
func installService(fips bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager (run as Administrator?): %w", err)
	}
	defer m.Disconnect()

	if s, err := m.OpenService(serviceName); err == nil {
		s.Close()
		return fmt.Errorf("service %q is already installed", serviceName)
	}

	s, err := m.CreateService(serviceName, exe, mgr.Config{
		DisplayName: serviceDisplay,
		Description: serviceDesc,
		StartType:   mgr.StartAutomatic,
		// Empty ServiceStartName => LocalSystem (what we need for DISM/scan/DPAPI).
	})
	if err != nil {
		return err
	}
	defer s.Close()

	// Auto-restart on failure.
	// Fast restart-on-failure so a taskkill is undone in ~5s (not 60s), without a
	// second guardian process (resource-light). The failure count resets after 10 min
	// of stability, so occasional kills keep getting restarted while a genuine
	// crash-loop still backs off.
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
	}, 600 /* reset failure count after 10 min */); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not set recovery actions:", err)
	}

	if fips {
		if err := setServiceFIPSEnv(); err != nil {
			fmt.Fprintln(os.Stderr, "warning: could not set FIPS service environment:", err)
		} else {
			fmt.Printf("FIPS 140-3 mode ENABLED for the service (GODEBUG=fips140=on)\n")
		}
	}

	fmt.Printf("installed service %q (LocalSystem, automatic start, restart-on-failure)\n", serviceName)
	return nil
}

// setServiceFIPSEnv writes GODEBUG=fips140=on into the service's Environment
// registry value (REG_MULTI_SZ), so the service process starts in FIPS mode
// without a separate binary or a machine-wide env var.
func setServiceFIPSEnv() error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\`+serviceName, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringsValue("Environment", []string{"GODEBUG=fips140=on"})
}

func uninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager (run as Administrator?): %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %q is not installed", serviceName)
	}
	defer s.Close()
	if err := s.Delete(); err != nil {
		return err
	}
	fmt.Printf("uninstalled service %q\n", serviceName)
	return nil
}

func startService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %q is not installed", serviceName)
	}
	defer s.Close()
	if err := s.Start(); err != nil {
		return err
	}
	fmt.Printf("service %q started\n", serviceName)
	return nil
}

func stopService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %q is not installed", serviceName)
	}
	defer s.Close()
	if _, err := s.Control(svc.Stop); err != nil {
		return err
	}
	fmt.Printf("stop requested for service %q\n", serviceName)
	return nil
}
