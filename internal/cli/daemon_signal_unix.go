//go:build !windows

package cli

import (
	"fmt"
	"syscall"

	"github.com/Muti-Metroo/Mutiauk/internal/config"
	"github.com/Muti-Metroo/Mutiauk/internal/daemon"
)

// signalReloadFallback reloads the daemon when the control socket is
// unavailable. On Unix this sends SIGHUP to the PID-file process.
func signalReloadFallback(cfg *config.Config) error {
	pid, err := daemon.ReadPIDFile(cfg.Daemon.PIDFile)
	if err != nil {
		return fmt.Errorf("daemon not running or PID file not found: %w", err)
	}
	return signalProcess(pid, syscall.SIGHUP, "SIGHUP")
}
