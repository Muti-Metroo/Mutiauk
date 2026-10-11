//go:build windows

package cli

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// stillActive is the GetExitCodeProcess return value for a running
// process (STILL_ACTIVE / STATUS_PENDING).
const stillActive = 259

// pidAlive reports whether pid refers to a live process. os.FindProcess
// always "succeeds" on Windows, so query the process exit code instead:
// STILL_ACTIVE means it is running.
func pidAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// gracefulStopFallback has no signal equivalent on Windows; graceful
// shutdown must go through the control socket (daemon.stop) or the SCM.
func gracefulStopFallback(_ int) error {
	return fmt.Errorf("daemon control socket unavailable; cannot request graceful stop on Windows (use 'mutiauk service stop' or ensure the daemon is reachable)")
}

// forceKill force-terminates the process via TerminateProcess.
func forceKill(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("failed to open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	if err := windows.TerminateProcess(h, 1); err != nil {
		return fmt.Errorf("failed to terminate process %d: %w", pid, err)
	}
	fmt.Printf("Terminated daemon (PID %d)\n", pid)
	return nil
}
