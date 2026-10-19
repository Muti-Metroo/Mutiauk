package cli

import (
	"fmt"
	"os"

	"github.com/Muti-Metroo/Mutiauk/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Configuration file commands",
	}
	cmd.AddCommand(newConfigCheckCmd())
	cmd.AddCommand(newConfigGenerateCmd())
	return cmd
}

// newConfigGenerateCmd builds `config generate`: the non-interactive
// counterpart to the `setup` wizard. It assembles a config from
// baseline defaults, an optional --from answers file, and per-field
// flag overrides, then writes it at mode 0600. Service installation is
// left to `mutiauk service install`.
func newConfigGenerateCmd() *cobra.Command {
	var opts generateOpts

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate a config file non-interactively (Ansible/Salt/cloud-init)",
		Long: `Assemble and write a config file without prompts.

Layers, in increasing precedence: built-in defaults, the --from answers
file, then per-field flags. The answers file may be a partial config (only
the keys you want to override) or a full existing config; unknown keys are
rejected. The result is validated and written at mode 0600.

  Example:
    cat <<EOF > /tmp/answers.yaml
    tun: { name: tun0, address: 10.7.0.1/24 }
    socks5: { server: edge.local:1080, username: ops }
    autoroutes: { enabled: true }
    EOF
    MUTIAUK_SOCKS5_PASSWORD=secret \
      sudo mutiauk config generate --from /tmp/answers.yaml -o /etc/mutiauk/config.yaml

To register the systemd service afterwards:
    sudo mutiauk service install -c /etc/mutiauk/config.yaml

Secrets (socks5.password) should be supplied via the MUTIAUK_SOCKS5_PASSWORD
env var rather than the answers file or command line.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigGenerate(opts)
		},
	}

	cmd.Flags().StringVar(&opts.answersPath, "from", "", "partial or full config.yaml supplying answers (Helm `--values`-style)")
	cmd.Flags().StringVarP(&opts.outputPath, "output", "o", "", "where to write the config (default /etc/mutiauk/config.yaml)")
	cmd.Flags().BoolVar(&opts.force, "force", false, "overwrite an existing output file")
	cmd.Flags().BoolVar(&opts.jsonOutput, "json", false, "emit a {path,mode,validated} report on stdout")

	cmd.Flags().StringVar(&opts.tunName, "tun-name", "", "override tun.name")
	cmd.Flags().StringVar(&opts.tunAddress, "tun-address", "", "override tun.address (IPv4 CIDR)")
	cmd.Flags().StringVar(&opts.tunAddress6, "tun-address6", "", "override tun.address6 (IPv6 CIDR)")
	cmd.Flags().StringVar(&opts.socks5Server, "socks5-server", "", "override socks5.server (host:port, or wss://host:port/path)")
	cmd.Flags().StringVar(&opts.socks5Username, "socks5-username", "", "override socks5.username (use MUTIAUK_SOCKS5_PASSWORD for the password)")
	cmd.Flags().BoolVar(&opts.autoroutesEnabled, "autoroutes-enabled", false, "enable autoroutes (receive routes over the SOCKS5 endpoint)")

	return cmd
}

// configCheckReport is the JSON shape returned by `config check --json`.
// Designed for CI gating: callers can branch on Errors/Warnings without
// parsing the human text.
type configCheckReport struct {
	Path     string   `json:"path"`
	OK       bool     `json:"ok"`
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

func newConfigCheckCmd() *cobra.Command {
	var jsonOutput bool
	var strict bool

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate the configuration file",
		Long: `Parse the config file at -c <path>, run all validation checks,
and report problems. Returns exit code 1 on errors. Warnings (e.g. legacy
'dns:' block, world-readable file mode) do not fail the check unless
--strict is passed.

Suitable for CI gating: 'mutiauk config check -c production.yaml --json'.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			report := buildConfigCheckReport(cfgFile, strict)

			if jsonOutput {
				if err := emitJSON(os.Stdout, report); err != nil {
					return err
				}
			} else {
				printConfigCheckReport(report)
			}

			if !report.OK {
				os.Exit(1)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output the report as JSON")
	cmd.Flags().BoolVar(&strict, "strict", false, "treat warnings as errors")

	return cmd
}

// buildConfigCheckReport runs every check the config layer exposes
// today, plus the file-mode probe. Errors fail the report; warnings
// only fail under --strict.
func buildConfigCheckReport(path string, strict bool) configCheckReport {
	r := configCheckReport{Path: path, OK: true}

	cfg, err := config.Load(path)
	if err != nil {
		r.Errors = append(r.Errors, err.Error())
		r.OK = false
		return r
	}

	if err := cfg.Validate(); err != nil {
		r.Errors = append(r.Errors, err.Error())
	}

	if config.LegacyDNSBlockPresent(path) {
		r.Warnings = append(r.Warnings,
			"legacy 'dns:' block present; the daemon ignores it. Mesh DNS now follows autoroutes + the agent's mesh_hosts_suffixes.")
	}

	if w := config.CheckFileMode(path); w != "" {
		r.Warnings = append(r.Warnings, w)
	}

	for i, route := range cfg.Routes {
		if route.EnabledWasDefaulted() {
			r.Warnings = append(r.Warnings, fmt.Sprintf(
				"routes[%d] (%s): missing explicit 'enabled' key; defaulted to true. Add 'enabled: true' to make intent explicit.",
				i, route.Destination,
			))
		}
	}

	if len(r.Errors) > 0 || (strict && len(r.Warnings) > 0) {
		r.OK = false
	}
	return r
}

func printConfigCheckReport(r configCheckReport) {
	fmt.Printf("Config: %s\n", r.Path)

	if len(r.Errors) == 0 && len(r.Warnings) == 0 {
		fmt.Println("OK - no issues found.")
		return
	}

	for _, e := range r.Errors {
		fmt.Printf("  [ERROR]   %s\n", e)
	}
	for _, w := range r.Warnings {
		fmt.Printf("  [WARNING] %s\n", w)
	}

	fmt.Println()
	if r.OK {
		fmt.Printf("OK - %d warning(s).\n", len(r.Warnings))
	} else {
		fmt.Printf("FAIL - %d error(s), %d warning(s).\n", len(r.Errors), len(r.Warnings))
	}
}
