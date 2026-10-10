package cli

import (
	"fmt"
	"os"

	"github.com/postalsys/mutiauk/internal/config"
	"github.com/postalsys/mutiauk/internal/wizard"
	"github.com/postalsys/mutiauk/internal/wizard/prompt"
	"github.com/spf13/cobra"
)

func newSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Set up Mutiauk (interactive wizard)",
		Long: `Run the interactive setup wizard (requires a TTY).

Guides you through TUN, SOCKS5, autoroutes, optional manual routes, and
optional systemd service installation. Point it at an existing config to
edit it -- the current values become the prompt defaults and sections you
skip (expose, NAT, DNS, logging) are kept as-is.

  Example: sudo mutiauk setup

For unattended config generation (Ansible/Salt/cloud-init), use
'mutiauk config generate' instead.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			w := wizard.New()
			configPath, err := w.Run()
			if err != nil {
				return fmt.Errorf("setup failed: %w", err)
			}

			fmt.Println()
			fmt.Printf("Setup complete! Configuration saved to: %s\n", configPath)

			return offerPostSetupCheck(configPath)
		},
	}

	return cmd
}

// offerPostSetupCheck asks the user whether to run a connectivity probe
// against the freshly-written config and prints the result. A probe
// failure is surfaced in the printed status; it does not fail the
// setup command.
func offerPostSetupCheck(configPath string) error {
	fmt.Println()
	run, err := prompt.Confirm("Run a connectivity check now?", true)
	if err != nil {
		return fmt.Errorf("read confirmation: %w", err)
	}
	if !run {
		return nil
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load saved config for check: %v\n", err)
		return nil
	}

	// Scope the config-mismatch comparison in printHumanStatus to the
	// freshly-written path, then restore the invocation value.
	prev := cfgFile
	cfgFile = configPath
	defer func() { cfgFile = prev }()

	fmt.Println()
	result := gatherStatus(cfg, false)
	return printHumanStatus(result, cfg)
}
