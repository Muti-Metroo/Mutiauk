//go:build windows

package service

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// isRootImpl reports whether the process is running elevated (the
// Windows analogue of root). Creating the TUN adapter, writing routes,
// and editing the NRPT all require an elevated token.
func isRootImpl() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// installImpl registers the daemon with the Service Control Manager and
// starts it. The service runs `mutiauk daemon start -c <config>`,
// which detects the SCM environment and runs under svc.Run.
func installImpl(cfg ServiceConfig, execPath string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer m.Disconnect()

	if s, err := m.OpenService(cfg.Name); err == nil {
		s.Close()
		return fmt.Errorf("service %s is already installed", cfg.Name)
	}

	s, err := m.CreateService(cfg.Name, execPath, mgr.Config{
		DisplayName:  cfg.Name,
		Description:  cfg.Description,
		StartType:    mgr.StartAutomatic,
		ServiceType:  windows.SERVICE_WIN32_OWN_PROCESS,
		ErrorControl: mgr.ErrorNormal,
	}, "daemon", "start", "-c", cfg.ConfigPath)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()

	fmt.Printf("Created service: %s\n", cfg.Name)

	if err := s.Start(); err != nil {
		return fmt.Errorf("service created but failed to start: %w", err)
	}
	fmt.Printf("Started service: %s\n", cfg.Name)

	fmt.Println("\nService management commands:")
	fmt.Printf("  sc query %s            # Check status\n", cfg.Name)
	fmt.Printf("  sc stop %s             # Stop\n", cfg.Name)
	fmt.Printf("  sc start %s            # Start\n", cfg.Name)
	return nil
}

// uninstallImpl stops (best-effort) and deletes the service.
func uninstallImpl(serviceName string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %s is not installed", serviceName)
	}
	defer s.Close()

	// Best-effort stop; ignore "not running" errors.
	if status, err := s.Control(svc.Stop); err == nil {
		fmt.Printf("Stopped service: %s (state %s)\n", serviceName, stateString(status.State))
	}

	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	fmt.Printf("Removed service: %s\n", serviceName)
	return nil
}

// statusImpl returns a coarse status string.
func statusImpl(serviceName string) (string, error) {
	info, err := statusDetailedImpl(serviceName)
	if err != nil {
		return "", err
	}
	if !info.Installed {
		return "not installed", nil
	}
	if info.Active {
		return "running", nil
	}
	if info.SubState != "" {
		return info.SubState, nil
	}
	return "not running", nil
}

// statusDetailedImpl populates StatusInfo from the SCM: Installed when
// the service exists, Enabled when its start type is automatic, Active
// when its current state is Running.
func statusDetailedImpl(serviceName string) (StatusInfo, error) {
	m, err := mgr.Connect()
	if err != nil {
		return StatusInfo{}, fmt.Errorf("connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return StatusInfo{Installed: false}, nil
	}
	defer s.Close()

	out := StatusInfo{Installed: true}

	if conf, err := s.Config(); err == nil {
		out.Enabled = conf.StartType == mgr.StartAutomatic
	}

	status, err := s.Query()
	if err != nil {
		return out, nil
	}
	state := stateString(status.State)
	out.SubState = state
	out.ActiveState = state
	out.Active = status.State == svc.Running
	return out, nil
}

// isInstalledImpl reports whether the service exists in the SCM.
func isInstalledImpl(serviceName string) bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return false
	}
	s.Close()
	return true
}

// stateString renders an svc.State as a lowercase word matching the
// vocabulary used by the systemd/launchd backends ("running", "stopped").
func stateString(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start-pending"
	case svc.StopPending:
		return "stop-pending"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continue-pending"
	case svc.PausePending:
		return "pause-pending"
	case svc.Paused:
		return "paused"
	default:
		return strings.ToLower(fmt.Sprintf("state-%d", state))
	}
}
