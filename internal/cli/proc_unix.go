//go:build !windows

package cli

import (
	"fmt"
	"os"
	"syscall"
)

// pidAlive reports whether pid refers to a live process. On Unix,
// os.FindProcess always succeeds, so a signal-0 probe is the real check.
func pidAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// gracefulStopFallback asks the process to stop when the control socket
// is unavailable. On Unix this is SIGTERM.
func gracefulStopFallback(pid int) error {
	return signalProcess(pid, syscall.SIGTERM, "SIGTERM")
}

// forceKill force-terminates the process. On Unix this is SIGKILL.
func forceKill(pid int) error {
	return signalProcess(pid, syscall.SIGKILL, "SIGKILL")
}

func signalProcess(pid int, sig syscall.Signal, sigName string) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find process: %w", err)
	}
	if err := process.Signal(sig); err != nil {
		return fmt.Errorf("failed to send %s: %w", sigName, err)
	}
	fmt.Printf("Sent %s to daemon (PID %d)\n", sigName, pid)
	return nil
}
