package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/postalsys/mutiauk/internal/config"
)

var (
	cfgFile string
	verbose bool
	logger  *zap.Logger
)

var rootCmd = &cobra.Command{
	Use:   "mutiauk",
	Short: "TUN to SOCKS5 proxy agent",
	Long: `Mutiauk is a TUN-based traffic capture and forwarder for TCP + UDP
into a SOCKS5 proxy, with built-in route management.

New here? Run "sudo mutiauk setup" for an interactive wizard.

It can run in two modes:
  - Daemon mode: Creates a TUN interface and forwards traffic through SOCKS5
  - CLI mode: Manages route rules and configuration`,
	// On RunE error, print the error to stderr but skip the flag /
	// usage dump. The dump masks the actual error in operator
	// terminals and is useless when the failure is a config read or
	// daemon-not-running condition; users hit `--help` when they
	// want flag listings.
	SilenceUsage: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		var err error
		if verbose {
			logger, err = zap.NewDevelopment()
		} else {
			logger, err = zap.NewProduction()
		}
		if err != nil {
			return fmt.Errorf("failed to initialize logger: %w", err)
		}
		return nil
	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		if logger != nil {
			_ = logger.Sync()
		}
	},
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", config.DefaultPath, "config file path")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")

	rootCmd.AddCommand(newSetupCmd())
	rootCmd.AddCommand(newDaemonCmd())
	rootCmd.AddCommand(newRouteCmd())
	rootCmd.AddCommand(newHostCmd())
	rootCmd.AddCommand(newDNSCmd())
	rootCmd.AddCommand(newExposeCmd())
	rootCmd.AddCommand(newStatusCmd())
	rootCmd.AddCommand(newServiceCmd())
	rootCmd.AddCommand(newConfigCmd())
	rootCmd.AddCommand(newCleanupCmd())
	rootCmd.AddCommand(newCompletionCmd())
	rootCmd.AddCommand(newVersionCmd())
}

// Execute runs the root command
func Execute() error {
	return rootCmd.Execute()
}

// GetLogger returns the configured logger
func GetLogger() *zap.Logger {
	if logger == nil {
		logger, _ = zap.NewProduction()
	}
	return logger
}

// GetConfigFile returns the config file path
func GetConfigFile() string {
	return cfgFile
}
