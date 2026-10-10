package cli

import (
	"fmt"
	"os"
	"runtime"

	"github.com/postalsys/mutiauk/internal/config"
	"github.com/postalsys/mutiauk/internal/service"
	"github.com/spf13/cobra"
)

func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "System service management (Linux, macOS, and Windows)",
		Long: `Install, uninstall, and manage Mutiauk as a system service
(systemd on Linux, launchd on macOS, the Service Control Manager on
Windows).

Mutiauk requires elevated privileges for TUN interface creation, so it
must be installed as a system service (not a user service).`,
	}

	cmd.AddCommand(newServiceInstallCmd())
	cmd.AddCommand(newServiceUninstallCmd())
	cmd.AddCommand(newServiceStatusCmd())

	return cmd
}

func newServiceInstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install as systemd service",
		Long: `Install Mutiauk as a systemd service.

The service will be enabled and started automatically. It will also
start on system boot.

The config path is taken from the persistent --config / -c flag at
the root of the CLI; the path is baked into the systemd unit's
ExecStart and StateDirectory.

Example:
  sudo mutiauk -c /etc/mutiauk/config.yaml service install`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !service.Supported() {
				return fmt.Errorf("service management is not supported on %s", runtime.GOOS)
			}
			if !service.IsRoot() {
				return fmt.Errorf("must run elevated to install service\n\n%s", elevatedInstallHint())
			}

			cfg := service.DefaultConfig(cfgFile)
			return service.Install(cfg)
		},
	}

	return cmd
}

func newServiceUninstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall systemd service",
		Long: `Remove Mutiauk systemd service.

This will stop and disable the service, then remove the systemd unit file.

Example:
  sudo mutiauk service uninstall`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !service.Supported() {
				return fmt.Errorf("service management is not supported on %s", runtime.GOOS)
			}
			if !service.IsRoot() {
				return fmt.Errorf("must run elevated to uninstall service")
			}

			return service.Uninstall("mutiauk")
		},
	}

	return cmd
}

func newServiceStatusCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show systemd service status",
		Long: `Show the current status of the Mutiauk systemd service.

With --json, returns a structured payload suitable for fleet tooling:
{installed, active, enabled, sub_state, active_state}.

Example:
  mutiauk service status
  mutiauk service status --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !service.Supported() {
				return fmt.Errorf("service management is not supported on %s", runtime.GOOS)
			}

			info, err := service.StatusDetailed("mutiauk")
			if err != nil {
				return err
			}

			if jsonOutput {
				return emitJSON(os.Stdout, info)
			}

			if !info.Installed {
				fmt.Println("Service is not installed")
				fmt.Printf("\nTo install: %s\n", elevatedInstallHint())
				return nil
			}

			fmt.Printf("Installed:    %t\n", info.Installed)
			fmt.Printf("Active:       %t (%s)\n", info.Active, info.ActiveState)
			fmt.Printf("Enabled:      %t\n", info.Enabled)
			if info.SubState != "" {
				fmt.Printf("Sub-state:    %s\n", info.SubState)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output in JSON format")

	return cmd
}

// elevatedInstallHint returns the OS-appropriate command to install the
// service with the privileges it needs.
func elevatedInstallHint() string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("mutiauk -c %s service install   (from an elevated prompt)", config.DefaultPath)
	}
	return fmt.Sprintf("sudo mutiauk -c %s service install", config.DefaultPath)
}
