//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	serviceName    = "AegisOneAgent"
	serviceDisplay = "Aegis One Agent"
	serviceDesc    = "Aegis One endpoint management agent — patch detection and remediation."
)

// runAgentEntry runs as a Windows service when launched by the SCM, otherwise in
// the console (dev), stopping gracefully on Ctrl+C.
func runAgentEntry() {
	if isSvc, err := svc.IsWindowsService(); err == nil && isSvc {
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
	const accepted = svc.AcceptStop | svc.AcceptShutdown
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
		err = installService()
	case "uninstall":
		err = uninstallService()
	case "start":
		err = startService()
	case "stop":
		err = stopService()
	default:
		return false
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s failed: %v\n", cmd, err)
		os.Exit(1)
	}
	return true
}

func installService() error {
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
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 120 * time.Second},
	}, 86400 /* reset failure count after 1 day */); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not set recovery actions:", err)
	}

	fmt.Printf("installed service %q (LocalSystem, automatic start, restart-on-failure)\n", serviceName)
	return nil
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
