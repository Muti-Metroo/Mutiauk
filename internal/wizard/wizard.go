// Package wizard provides an interactive setup wizard for Mutiauk.
package wizard

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/postalsys/mutiauk/internal/config"
	"github.com/postalsys/mutiauk/internal/wizard/prompt"
	"gopkg.in/yaml.v3"
)

// Wizard handles the interactive setup process.
type Wizard struct {
	configPath       string
	serviceInstaller *ServiceInstaller

	// existingCfg is the config loaded from the chosen path when the
	// wizard is re-run on an existing file (defaults applied). It seeds
	// every prompt default so re-edited values show sensible numbers.
	existingCfg *config.Config

	// existingRaw is the same file parsed WITHOUT defaults and WITHOUT
	// the env-secret overrides (see config.ParseRaw). buildConfig clones
	// it as the edit base so re-saved configs stay minimal: only the
	// sections the operator actually set are non-zero, and untouched
	// sections (expose, nat tuning, dns_integration, daemon, logging)
	// are preserved verbatim rather than re-emitting every default.
	// Secret defaults (socks5.password) are read from here too, so a
	// wizard run with MUTIAUK_SOCKS5_PASSWORD set does not bake the env
	// secret into the written file.
	existingRaw *config.Config
}

// New creates a new setup wizard.
func New() *Wizard {
	return &Wizard{}
}

// Run executes the setup wizard and returns the generated config path.
func (w *Wizard) Run() (string, error) {
	prompt.PrintBanner("Mutiauk Setup Wizard", "TUN-based SOCKS5 Proxy Agent")

	fmt.Println("This wizard will help you configure Mutiauk.")
	fmt.Println("Press Enter to accept default values shown in [brackets].")
	fmt.Println("Tip: point it at an existing config to edit it; current values become the defaults.")
	fmt.Println()

	configPath, err := w.askConfigPath()
	if err != nil {
		return "", err
	}
	w.configPath = configPath

	tunConfig, err := w.askTUNConfig()
	if err != nil {
		return "", err
	}

	socks5Config, err := w.askSOCKS5Config()
	if err != nil {
		return "", err
	}

	autoRoutesConfig, useAutoRoutes, err := w.askAutoRoutes()
	if err != nil {
		return "", err
	}

	var routes []config.RouteConfig
	if !useAutoRoutes {
		routes, err = w.askRoutes()
		if err != nil {
			return "", err
		}
	}

	cfg := w.buildConfig(tunConfig, socks5Config, autoRoutesConfig, routes)
	if err := w.writeConfig(cfg, configPath); err != nil {
		return "", err
	}

	prompt.PrintSuccess(fmt.Sprintf("Configuration saved to %s", configPath))
	w.printSummary(cfg, configPath)

	w.serviceInstaller = NewServiceInstaller(configPath)
	if err := w.serviceInstaller.Run(); err != nil {
		return "", err
	}

	return configPath, nil
}

func (w *Wizard) askConfigPath() (string, error) {
	prompt.PrintHeader("Configuration File", "Where should the configuration file be saved?")

	defaultPath := config.DefaultPath

	configPath, err := prompt.ReadLineValidated("Config file path", defaultPath,
		Chain(Required("config path"), YAMLExtension()))
	if err != nil {
		return "", err
	}

	if err := w.ensureDirectoryExists(configPath); err != nil {
		return "", err
	}

	if err := w.loadExistingOrConfirmOverwrite(configPath); err != nil {
		return "", err
	}

	return configPath, nil
}

func (w *Wizard) ensureDirectoryExists(filePath string) error {
	dir := filepath.Dir(filePath)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		return nil
	}

	create, err := prompt.Confirm(fmt.Sprintf("Directory %s does not exist. Create it?", dir), true)
	if err != nil {
		return err
	}

	if !create {
		return nil
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	prompt.PrintSuccess(fmt.Sprintf("Created directory %s", dir))
	return nil
}

// loadExistingOrConfirmOverwrite handles a path that already exists.
// A file that parses as a valid config becomes the edit base: it is
// stored on the wizard so every subsequent prompt defaults to its
// values and buildConfig preserves the sections the wizard never asks
// about. A file that does not parse is treated as foreign content and
// only replaced after an explicit confirm.
func (w *Wizard) loadExistingOrConfirmOverwrite(filePath string) error {
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return nil
	}

	if cfg, err := config.Load(filePath); err == nil {
		w.existingCfg = cfg
		// Parse the same file without defaults / env overrides so
		// buildConfig can preserve only the operator-set sections and
		// keep env secrets out of the written file. Best-effort: if the
		// raw parse fails, leave it nil and fall back to the baseline.
		if data, readErr := os.ReadFile(filePath); readErr == nil {
			if raw, rawErr := config.ParseRaw(data); rawErr == nil {
				w.existingRaw = raw
			}
		}
		prompt.PrintInfo("Found existing configuration; using its values as defaults.")
		prompt.PrintInfo("Sections you skip (expose, NAT, DNS, logging) are kept as-is.")
		return nil
	}

	prompt.PrintWarning("A file exists at this path but is not a valid Mutiauk config.")
	overwrite, err := prompt.Confirm("Overwrite it?", false)
	if err != nil {
		return err
	}
	if !overwrite {
		return fmt.Errorf("setup cancelled")
	}

	return nil
}

// existingTUN/SOCKS5/AutoRoutes return the loaded config's section when
// the wizard is editing an existing file, or a zero value otherwise, so
// prompt defaults can be sourced with a single nil-safe accessor.
func (w *Wizard) existingTUN() config.TUNConfig {
	if w.existingCfg != nil {
		return w.existingCfg.TUN
	}
	return config.TUNConfig{}
}

func (w *Wizard) existingSOCKS5() config.SOCKS5Config {
	if w.existingCfg != nil {
		return w.existingCfg.SOCKS5
	}
	return config.SOCKS5Config{}
}

func (w *Wizard) existingAutoRoutes() config.AutoRoutesConfig {
	if w.existingCfg != nil {
		return w.existingCfg.AutoRoutes
	}
	return config.AutoRoutesConfig{}
}

// existingRawSOCKS5 returns the raw-parsed section (no defaults, no env
// overrides) so the "press Enter to keep current" secret paths default to
// the FILE value, not an env-injected one. A wizard run with
// MUTIAUK_SOCKS5_PASSWORD set must not persist the env secret into config.yaml.
func (w *Wizard) existingRawSOCKS5() config.SOCKS5Config {
	if w.existingRaw != nil {
		return w.existingRaw.SOCKS5
	}
	return config.SOCKS5Config{}
}

// orDefault returns existing when it is non-empty, else fallback. Used
// to seed prompt defaults from the loaded config without clobbering the
// wizard's built-in default when a field was never set.
func orDefault(existing, fallback string) string {
	if existing != "" {
		return existing
	}
	return fallback
}

func (w *Wizard) askTUNConfig() (config.TUNConfig, error) {
	prompt.PrintHeader("TUN Interface", "Configure the virtual network interface.")

	cfg := config.TUNConfig{}
	existing := w.existingTUN()

	mtuDefault := "1400"
	if existing.MTU != 0 {
		mtuDefault = fmt.Sprintf("%d", existing.MTU)
	}

	name, err := prompt.ReadLineValidated("Interface name", orDefault(existing.Name, "tun0"),
		Chain(Required("interface name"), MaxLength(15)))
	if err != nil {
		return cfg, err
	}
	cfg.Name = name

	mtuStr, err := prompt.ReadLineValidated("MTU", mtuDefault, IntRange(576, 65535, "MTU"))
	if err != nil {
		return cfg, err
	}
	fmt.Sscanf(mtuStr, "%d", &cfg.MTU)

	address, err := prompt.ReadLineValidated("IPv4 address (CIDR)", orDefault(existing.Address, "10.200.200.1/24"),
		Chain(Required("IPv4 address"), IPv4CIDR()))
	if err != nil {
		return cfg, err
	}
	cfg.Address = address

	useIPv6, err := prompt.Confirm("Configure IPv6 address?", existing.Address6 != "")
	if err != nil {
		return cfg, err
	}

	if useIPv6 {
		address6, err := prompt.ReadLineValidated("IPv6 address (CIDR)", orDefault(existing.Address6, "fd00:200::1/64"), IPv6CIDR())
		if err != nil {
			return cfg, err
		}
		cfg.Address6 = address6
	}

	return cfg, nil
}

func (w *Wizard) askSOCKS5Config() (config.SOCKS5Config, error) {
	prompt.PrintHeader("SOCKS5 Proxy", "Configure the upstream SOCKS5 proxy server.")

	cfg := config.SOCKS5Config{}
	existing := w.existingSOCKS5()

	server, err := prompt.ReadLineValidated("SOCKS5 server address", orDefault(existing.Server, "127.0.0.1:1080"),
		Chain(Required("server address"), HostPort()))
	if err != nil {
		return cfg, err
	}
	cfg.Server = server

	useAuth, err := prompt.Confirm("Does the SOCKS5 server require authentication?", existing.Username != "")
	if err != nil {
		return cfg, err
	}

	if useAuth {
		username, err := prompt.ReadLineValidated("Username", existing.Username, Required("username"))
		if err != nil {
			return cfg, err
		}
		cfg.Username = username

		// Source the keep-current default from the raw (file) value, not
		// the env-overridden existingCfg, so MUTIAUK_SOCKS5_PASSWORD is
		// never baked into the written config.
		rawPassword := w.existingRawSOCKS5().Password
		passPrompt := "Password"
		if rawPassword != "" {
			passPrompt = "Password (press Enter to keep current)"
		}
		password, err := prompt.ReadPassword(passPrompt)
		if err != nil {
			return cfg, err
		}
		// Empty input keeps the existing password on a re-run rather than
		// silently clearing it (ReadPassword has no echoed default).
		if password == "" {
			password = rawPassword
		}
		cfg.Password = password
	}

	return cfg, nil
}

func (w *Wizard) askAutoRoutes() (config.AutoRoutesConfig, bool, error) {
	prompt.PrintHeader("Automatic Routes", "Receive routes from the Muti Metroo agent over SOCKS5.")

	fmt.Println("Autoroutes receives and maintains routes, mesh hosts, and DNS suffixes")
	fmt.Println("from the Muti Metroo agent over the same SOCKS5 endpoint used for traffic.")
	fmt.Println("This eliminates the need to manually manage routes.")
	fmt.Println()

	// Autoroutes is the recommended setup, so a fresh run defaults to
	// enabled. When editing an existing config, honour whatever it had.
	autoDefault := true
	if w.existingCfg != nil {
		autoDefault = w.existingAutoRoutes().Enabled
	}
	useAutoRoutes, err := prompt.Confirm("Enable automatic routes from the Muti Metroo agent?", autoDefault)
	if err != nil {
		return config.AutoRoutesConfig{}, false, err
	}
	if !useAutoRoutes {
		return config.AutoRoutesConfig{}, false, nil
	}

	return config.AutoRoutesConfig{Enabled: true}, true, nil
}

func (w *Wizard) askRoutes() ([]config.RouteConfig, error) {
	prompt.PrintHeader("Routes", "Configure which traffic to route through the SOCKS5 proxy.")

	fmt.Println("Enter destination CIDRs to route through the proxy.")
	fmt.Println("Common examples:")
	fmt.Println("  10.0.0.0/8       - Private class A networks")
	fmt.Println("  192.168.0.0/16   - Private class C networks")
	fmt.Println("  172.16.0.0/12    - Private class B networks")
	fmt.Println()

	var routes []config.RouteConfig

	// On a re-run with manual routes already configured, keep them as the
	// starting set and skip the preset offer; the operator only appends.
	hasExisting := w.existingCfg != nil && len(w.existingCfg.Routes) > 0
	if hasExisting {
		routes = append(routes, w.existingCfg.Routes...)
		prompt.PrintInfo(fmt.Sprintf("Keeping %d existing route(s):", len(routes)))
		for _, r := range routes {
			label := r.Destination
			if r.Comment != "" {
				label += "  # " + r.Comment
			}
			if !r.Enabled {
				label += "  (disabled)"
			}
			fmt.Printf("  - %s\n", label)
		}
		fmt.Println()
	}

	usePresets := false
	if !hasExisting {
		var err error
		usePresets, err = prompt.Confirm("Use common private network routes (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16)?", true)
		if err != nil {
			return nil, err
		}
		if usePresets {
			routes = append(routes, defaultPrivateRoutes()...)
			prompt.PrintSuccess("Added standard private network routes")
		}
	}

	routes, err := w.addCustomRoutes(routes, usePresets || hasExisting)
	if err != nil {
		return nil, err
	}

	if len(routes) == 0 {
		prompt.PrintWarning("No routes configured. Mutiauk will not forward any traffic.")
	}

	return routes, nil
}

func defaultPrivateRoutes() []config.RouteConfig {
	return []config.RouteConfig{
		{Destination: "10.0.0.0/8", Comment: "Private class A", Enabled: true},
		{Destination: "172.16.0.0/12", Comment: "Private class B", Enabled: true},
		{Destination: "192.168.0.0/16", Comment: "Private class C", Enabled: true},
	}
}

func (w *Wizard) addCustomRoutes(routes []config.RouteConfig, hasPresets bool) ([]config.RouteConfig, error) {
	for {
		defaultAdd := !hasPresets && len(routes) == 0
		addCustom, err := prompt.Confirm("Add a custom route?", defaultAdd)
		if err != nil {
			return nil, err
		}

		if !addCustom {
			break
		}

		route, err := w.askSingleRoute()
		if err != nil {
			return nil, err
		}

		routes = append(routes, route)
		prompt.PrintSuccess(fmt.Sprintf("Added route: %s", route.Destination))
	}

	return routes, nil
}

func (w *Wizard) askSingleRoute() (config.RouteConfig, error) {
	cidr, err := prompt.ReadLineValidated("Destination CIDR", "",
		Chain(Required("CIDR"), CIDR()))
	if err != nil {
		return config.RouteConfig{}, err
	}

	comment, err := prompt.ReadLine("Comment (optional)", "")
	if err != nil {
		return config.RouteConfig{}, err
	}

	return config.RouteConfig{
		Destination: cidr,
		Comment:     comment,
		Enabled:     true,
	}, nil
}

func (w *Wizard) buildConfig(tunCfg config.TUNConfig, socks5Cfg config.SOCKS5Config, autoRoutesCfg config.AutoRoutesConfig, routes []config.RouteConfig) *config.Config {
	// Editing an existing config: start from the RAW parse (no defaults,
	// no env overrides) so sections the wizard never asks about (expose,
	// NAT tuning, dns_integration, daemon, logging) are preserved exactly
	// as the operator wrote them, and re-saved configs do not balloon
	// with every default value. A fresh setup starts from the baseline
	// shape instead. existingRaw can be nil if the raw parse failed even
	// though Load succeeded; fall back to the baseline in that case.
	var cfg *config.Config
	if w.existingRaw != nil {
		clone := *w.existingRaw
		cfg = &clone
	} else {
		cfg = BuildBaselineConfig()
	}
	cfg.TUN = tunCfg
	cfg.SOCKS5 = socks5Cfg
	cfg.AutoRoutes = autoRoutesCfg
	cfg.Routes = routes

	if cfg.SOCKS5.Timeout == 0 {
		cfg.SOCKS5.Timeout = 30 * time.Second
	}
	if cfg.SOCKS5.KeepAlive == 0 {
		cfg.SOCKS5.KeepAlive = time.Minute
	}

	return cfg
}

// BuildBaselineConfig returns a Config populated with the defaults
// the wizard installs for any new deployment: PID/socket paths,
// production NAT and logging defaults, no TUN/SOCKS5 specifics.
// Used by both the interactive wizard and the non-interactive
// setup path so the two share one source of truth for the baseline
// shape of a fresh /etc/mutiauk/config.yaml.
func BuildBaselineConfig() *config.Config {
	return &config.Config{
		Daemon: config.DaemonConfig{
			PIDFile:    "/var/run/mutiauk.pid",
			SocketPath: "/var/run/mutiauk.sock",
		},
		NAT: config.NATConfig{
			TableSize:  65536,
			TCPTimeout: time.Hour,
			UDPTimeout: 5 * time.Minute,
			GCInterval: time.Minute,
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
	}
}

func (w *Wizard) writeConfig(cfg *config.Config, path string) error {
	return WriteConfigFile(cfg, path)
}

// WriteConfigFile marshals cfg to YAML, prepends the wizard's header
// comment, ensures the parent directory exists, and writes the result
// at mode 0600 (re-chmoding any pre-existing file at the same path
// since os.WriteFile only sets the mode on file creation).
//
// 0600 is required because the config carries socks5.password. Both
// the interactive wizard and the non-interactive
// `mutiauk setup --from ...` path go through here so the on-disk
// artefact is bit-identical.
func WriteConfigFile(cfg *config.Config, path string) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	header := `# Mutiauk Configuration
# Generated by mutiauk (setup / config generate)
#
# To start Mutiauk:
#   sudo mutiauk daemon start -c ` + path + `
#
# To install as a service:
#   sudo mutiauk -c ` + path + ` service install
#

`

	content := header + string(data)

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	if err := os.WriteFile(path, []byte(content), config.DefaultFileMode); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}
	if err := os.Chmod(path, config.DefaultFileMode); err != nil {
		return fmt.Errorf("failed to chmod config: %w", err)
	}

	return nil
}

func (w *Wizard) printSummary(cfg *config.Config, configPath string) {
	prompt.PrintHeader("Configuration Summary", "")

	fmt.Printf("  Config file:     %s\n", configPath)
	fmt.Printf("  TUN interface:   %s\n", cfg.TUN.Name)
	fmt.Printf("  TUN address:     %s\n", cfg.TUN.Address)
	if cfg.TUN.Address6 != "" {
		fmt.Printf("  TUN IPv6:        %s\n", cfg.TUN.Address6)
	}
	fmt.Printf("  MTU:             %d\n", cfg.TUN.MTU)
	fmt.Printf("  SOCKS5 server:   %s\n", cfg.SOCKS5.Server)
	if cfg.SOCKS5.Username != "" {
		fmt.Printf("  SOCKS5 auth:     yes (user: %s)\n", cfg.SOCKS5.Username)
	}

	w.printRouteSummary(cfg)

	if cfg.AutoRoutes.Enabled {
		fmt.Printf("  Mesh DNS:        follows autoroutes; suffixes come from the agent.\n")
		fmt.Printf("                   systemd-resolved is configured automatically on each poll.\n")
	}

	fmt.Println()
	fmt.Println("To start Mutiauk manually:")
	fmt.Printf("  sudo mutiauk daemon start -c %s\n", configPath)
	fmt.Println()
}

func (w *Wizard) printRouteSummary(cfg *config.Config) {
	if cfg.AutoRoutes.Enabled {
		fmt.Printf("  Autoroutes:      enabled (routes received over the SOCKS5 endpoint)\n")
		return
	}

	if len(cfg.Routes) == 0 {
		fmt.Printf("  Routes:          none configured\n")
		return
	}

	fmt.Printf("  Routes:          %d configured\n", len(cfg.Routes))
	for _, r := range cfg.Routes {
		comment := ""
		if r.Comment != "" {
			comment = fmt.Sprintf(" (%s)", r.Comment)
		}
		fmt.Printf("                   - %s%s\n", r.Destination, comment)
	}
}
