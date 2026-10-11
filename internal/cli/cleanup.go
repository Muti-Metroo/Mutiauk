package cli

import (
	"fmt"
	"os"

	"github.com/postalsys/mutiauk/internal/config"
	meshdns "github.com/postalsys/mutiauk/internal/dns"
	"github.com/postalsys/mutiauk/internal/route"
	"github.com/postalsys/mutiauk/internal/service"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// routeStateFile is the OS-appropriate persisted-route state-file path,
// kept in sync with internal/route.
var routeStateFile = route.DefaultStateFile()

func newCleanupCmd() *cobra.Command {
	var purge bool
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove TUN interface, stale PID file and socket",
		Long: `Reset the host to a clean state after a crash or unclean shutdown.

Removes:
  - Stale host-DNS integration state (resolvectl per-link config,
    nm-dnsmasq drop-in, macOS /etc/resolver files, Windows NRPT rules)
  - The TUN interface (also clears associated kernel routes)
  - The PID file (if stale)
  - The API Unix socket (if stale)

Refuses to run if the daemon is currently active; stop it first with
"sudo mutiauk daemon stop".

Use --purge to also delete the persisted route state file at
` + routeStateFile + `.

Use --dry-run to preview the actions without touching the system.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if isDaemonRunning(cfg) {
				return fmt.Errorf("daemon is running; stop it first with: sudo mutiauk daemon stop")
			}

			runCleanup(cfg, purge, dryRun)
			return nil
		},
	}

	cmd.Flags().BoolVar(&purge, "purge", false, "also remove persisted route state file")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be removed without touching the system")

	return cmd
}

func runCleanup(cfg *config.Config, purge, dryRun bool) {
	if dryRun {
		fmt.Println("Dry run - no changes will be made.")
	}
	revertDNSIntegrations(cfg.TUN.Name, dryRun)
	removeTUNAdapter(cfg.TUN.Name, dryRun)
	removeFileIfPresent(cfg.Daemon.PIDFile, "PID file", dryRun)
	removeFileIfPresent(cfg.Daemon.SocketPath, "API socket", dryRun)
	if purge {
		removeFileIfPresent(routeStateFile, "route state file", dryRun)
	}
	if dryRun {
		fmt.Println("Dry run complete; rerun without --dry-run to apply.")
	} else {
		fmt.Println("Cleanup complete.")
	}
}

// revertDNSIntegrations clears any host-DNS state the daemon may have
// installed (systemd-resolved per-link config, NetworkManager dnsmasq
// drop-in, macOS /etc/resolver files, Windows NRPT rules). Best-effort
// across all known backends; missing tooling or pristine state each
// surface as "skipped" rather than an error.
func revertDNSIntegrations(iface string, dryRun bool) {
	if !service.IsRoot() {
		fmt.Println("DNS integration revert: skipped (requires elevated privileges)")
		return
	}
	if dryRun {
		fmt.Println("DNS integration: would revert (best-effort across all host backends)")
		return
	}
	meshdns.RevertAll(iface, zap.NewNop())
	fmt.Println("DNS integration: reverted (best-effort across all host backends)")
}

func removeFileIfPresent(path, label string, dryRun bool) {
	if path == "" {
		return
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("%s %s: not present (skipped)\n", label, path)
			return
		}
		fmt.Printf("%s %s: failed to stat: %v\n", label, path, err)
		return
	}
	if dryRun {
		fmt.Printf("%s %s: would remove\n", label, path)
		return
	}
	if err := os.Remove(path); err != nil {
		fmt.Printf("%s %s: failed to remove: %v\n", label, path, err)
		return
	}
	fmt.Printf("%s %s: removed\n", label, path)
}
