package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/postalsys/mutiauk/internal/api"
	"github.com/postalsys/mutiauk/internal/config"
	"github.com/postalsys/mutiauk/internal/daemon"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Daemon management commands",
	}

	cmd.AddCommand(newDaemonStartCmd())
	cmd.AddCommand(newDaemonStopCmd())
	cmd.AddCommand(newDaemonReloadCmd())
	cmd.AddCommand(newDaemonStatusCmd())

	return cmd
}

// newDaemonStatusCmd is a thin pid/uptime/config_path probe suitable
// for k8s readiness checks and orchestrator polling. The top-level
// `mutiauk status` is heavier (runs SOCKS5/TCP/UDP probes) and
// inappropriate for high-frequency monitoring.
func newDaemonStatusCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show lightweight daemon status (no network probes)",
		Long: `Print whether the daemon process is alive, its PID, uptime, and
the config path it started with. Does not run any network probes; for
TCP/UDP/SOCKS5 connectivity tests use the top-level 'mutiauk status'.

Suitable for high-frequency monitoring (k8s readiness, Nagios) since
each call is a single Unix-socket round trip with no kernel queries.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			result, err := fetchDaemonStatus(cfg)
			if err != nil {
				if jsonOutput {
					return printDaemonStatusJSON(&api.StatusResult{Running: false})
				}
				fmt.Println("Daemon: not running")
				os.Exit(1)
			}
			if jsonOutput {
				return printDaemonStatusJSON(result)
			}
			printDaemonStatusHuman(result)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output in JSON format")
	return cmd
}

// fetchDaemonStatus tries the API socket first; on failure it falls
// back to a PID-file + signal-0 probe so a stuck daemon still answers
// "running, but unhealthy" rather than the more confusing "not
// running" we'd otherwise print.
func fetchDaemonStatus(cfg *config.Config) (*api.StatusResult, error) {
	client := api.NewClient(cfg.Daemon.SocketPath)
	if client.IsRunning() {
		return client.Status()
	}
	pid, err := daemon.ReadPIDFile(cfg.Daemon.PIDFile)
	if err != nil {
		return nil, fmt.Errorf("daemon not running")
	}
	if !pidAlive(pid) {
		return nil, fmt.Errorf("daemon not running (stale PID file)")
	}
	return &api.StatusResult{
		Running:    true,
		PID:        pid,
		ConfigPath: cfgFile,
	}, nil
}

func printDaemonStatusJSON(r *api.StatusResult) error {
	return emitJSON(os.Stdout, r)
}

func printDaemonStatusHuman(r *api.StatusResult) {
	fmt.Println("Daemon: running")
	if r.PID != 0 {
		fmt.Printf("PID:    %d\n", r.PID)
	}
	if r.Uptime != "" {
		fmt.Printf("Uptime: %s\n", r.Uptime)
	}
	if r.ConfigPath != "" {
		fmt.Printf("Config: %s\n", r.ConfigPath)
	}
}

func newDaemonStartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the daemon",
		Long: `Start the mutiauk daemon in the foreground.

The daemon does not self-daemonize; backgrounding is the service
manager's job (systemd on Linux, launchd on macOS, the Service Control
Manager on Windows). Use 'mutiauk service install' to install the
bundled service unit. When launched by the Windows SCM, the daemon
detects the service environment automatically.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			log := GetLogger()

			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid config: %w", err)
			}

			if config.LegacyDNSBlockPresent(cfgFile) {
				log.Warn("ignoring legacy 'dns:' block in config; mesh DNS now follows " +
					"autoroutes and the suffix list comes from the agent's mesh_hosts_suffixes")
			}

			if w := config.CheckFileMode(cfgFile); w != "" {
				log.Warn(w)
			}

			warnDefaultedRoutes(log, cfg)

			log.Info("starting mutiauk daemon",
				zap.String("config", cfgFile),
				zap.String("tun", cfg.TUN.Name),
				zap.String("socks5", cfg.SOCKS5.Server),
			)

			srv, err := daemon.New(cfg, cfgFile, log)
			if err != nil {
				return fmt.Errorf("failed to create daemon: %w", err)
			}

			// runDaemonLifecycle owns the run context and the
			// platform-specific shutdown/reload triggers (Unix signals,
			// or the Windows Service Control Manager when launched as a
			// service). It returns only after the daemon has stopped.
			if err := runDaemonLifecycle(log, srv); err != nil {
				return err
			}

			log.Info("daemon stopped")
			return nil
		},
	}

	return cmd
}

// warnDefaultedRoutes emits one WARN log entry per route whose
// `enabled:` key was omitted in the YAML and was therefore defaulted
// to true. This surfaces in journald so operators auditing legacy
// configs can spot routes that previously got the silent zero-value
// (false) treatment.
func warnDefaultedRoutes(log *zap.Logger, cfg *config.Config) {
	for _, r := range cfg.Routes {
		if r.EnabledWasDefaulted() {
			log.Warn("route missing explicit 'enabled' key; defaulted to true",
				zap.String("destination", r.Destination),
				zap.String("comment", r.Comment),
			)
		}
	}
}

func newDaemonStopCmd() *cobra.Command {
	var wait bool
	var timeout time.Duration
	var force bool

	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon",
		Long: `Ask the running daemon to shut down gracefully.

The stop request goes over the control socket (works on all platforms);
on Linux/macOS it falls back to SIGTERM when the socket is unavailable.
By default the command blocks until the daemon exits (or --timeout
elapses), so 'daemon stop && daemon start' is race-free. Use --wait=false
for fire-and-forget behavior. Use --force to force-terminate the process
(SIGKILL on Unix, TerminateProcess on Windows) if it does not exit within
--timeout.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			pid, err := daemon.ReadPIDFile(cfg.Daemon.PIDFile)
			if err != nil {
				return fmt.Errorf("daemon not running or PID file not found: %w", err)
			}

			if err := requestStop(cfg, pid); err != nil {
				return err
			}

			if !wait {
				return nil
			}

			if waitForDaemonExit(cfg, pid, timeout) {
				fmt.Printf("Daemon (PID %d) stopped\n", pid)
				return nil
			}

			if !force {
				return fmt.Errorf("daemon (PID %d) did not exit within %s; rerun with --force to force-terminate", pid, timeout)
			}

			fmt.Printf("Daemon (PID %d) did not exit within %s; force-terminating\n", pid, timeout)
			if err := forceKill(pid); err != nil {
				return err
			}
			if !waitForDaemonExit(cfg, pid, 5*time.Second) {
				return fmt.Errorf("daemon (PID %d) still alive 5s after force-terminate; investigate manually", pid)
			}
			fmt.Printf("Daemon (PID %d) killed\n", pid)
			return nil
		},
	}

	cmd.Flags().BoolVar(&wait, "wait", true, "block until the daemon exits (or --timeout elapses)")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "how long to wait for graceful shutdown before --force escalation")
	cmd.Flags().BoolVar(&force, "force", false, "force-terminate the daemon if it does not exit within --timeout")

	return cmd
}

func newDaemonReloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "Reload daemon configuration",
		Long: `Ask the running daemon to reload and apply its on-disk config.

The reload request goes over the control socket (works on all platforms);
on Linux/macOS it falls back to SIGHUP when the socket is unavailable.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			client := api.NewClient(cfg.Daemon.SocketPath)
			if client.IsRunning() {
				res, err := client.Reload()
				if err != nil {
					return err
				}
				if !res.Success {
					return fmt.Errorf("reload rejected: %s", res.Message)
				}
				fmt.Println("Configuration reloaded")
				return nil
			}
			return signalReloadFallback(cfg)
		},
	}
}

// requestStop asks the daemon to shut down gracefully. It prefers the
// control socket (the daemon.stop method, available on every platform);
// when the socket is unreachable it falls back to a platform-specific
// graceful signal (SIGTERM on Unix, an error on Windows where there is
// no signal path).
func requestStop(cfg *config.Config, pid int) error {
	client := api.NewClient(cfg.Daemon.SocketPath)
	if client.IsRunning() {
		if err := client.Stop(); err != nil {
			return err
		}
		fmt.Printf("Requested graceful shutdown of daemon (PID %d)\n", pid)
		return nil
	}
	return gracefulStopFallback(pid)
}

// waitForDaemonExit polls until the daemon process is gone or
// timeout elapses. Returns true if the process exited, false on
// timeout. Used by `daemon stop --wait` to make
// "stop && start" race-free.
//
// We check both the captured PID and isDaemonRunning so we don't return
// early before the daemon has finished tearing down its listeners. The
// captured PID is checked directly (not via the PID file) to sidestep
// the daemon's own PID-file removal during teardown.
func waitForDaemonExit(cfg *config.Config, pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !pidAlive(pid) && !isDaemonRunning(cfg) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}
