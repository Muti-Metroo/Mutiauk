//go:build windows

package cli

import (
	"fmt"

	"github.com/Muti-Metroo/Mutiauk/internal/config"
)

// signalReloadFallback has no signal equivalent on Windows; reload must
// go through the control socket (daemon.reload).
func signalReloadFallback(_ *config.Config) error {
	return fmt.Errorf("daemon control socket unavailable; cannot reload on Windows (ensure the daemon is reachable)")
}
