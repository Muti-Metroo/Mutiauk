package cli

import (
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/postalsys/mutiauk/internal/config"
	"github.com/postalsys/mutiauk/internal/wizard"
)

// generateOpts collects all the inputs to the non-interactive config
// generation path (`mutiauk config generate`). Empty strings mean
// "no override"; per-field flags layer on top of the answers file via
// applyTo.
type generateOpts struct {
	answersPath string
	outputPath  string
	force       bool
	jsonOutput  bool

	// Per-field overrides. Strings only -- duration/integer fields
	// belong in the answers file because they're rarely templated
	// per host and would balloon the flag surface.
	tunName           string
	tunAddress        string
	tunAddress6       string
	socks5Server      string
	socks5Username    string
	autoroutesEnabled bool
}

// generateReport is the JSON payload returned by `config generate --json`.
// Designed for Ansible / Salt task results -- every field has a stable,
// scalar shape so playbooks can branch with `register: result` cleanly.
type generateReport struct {
	Path      string `json:"path"`
	Mode      string `json:"mode"`
	Validated bool   `json:"validated"`
}

// runConfigGenerate writes the canonical 0600 config from flags/answers.
// Note: secrets (socks5.password) are not handled here -- they enter via
// env vars at config.Load time, so the generated YAML never carries them.
// Service installation is deliberately left to `mutiauk service install`.
func runConfigGenerate(opts generateOpts) error {
	if opts.outputPath == "" {
		opts.outputPath = config.DefaultPath
	}

	cfg, err := assembleConfig(opts)
	if err != nil {
		return err
	}

	// Apply package defaults before validating so fields the operator
	// hasn't touched (e.g. daemon.status_probe_target) don't surface
	// as spurious "missing port" errors.
	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config validation failed: %w", err)
	}

	if !opts.force {
		if _, err := os.Stat(opts.outputPath); err == nil {
			return fmt.Errorf("refusing to overwrite existing config %s (pass --force to overwrite)", opts.outputPath)
		}
	}

	if err := wizard.WriteConfigFile(cfg, opts.outputPath); err != nil {
		return err
	}

	report := generateReport{
		Path:      opts.outputPath,
		Mode:      fmt.Sprintf("%#o", config.DefaultFileMode),
		Validated: true,
	}

	return printGenerateReport(os.Stdout, report, opts.jsonOutput)
}

// assembleConfig layers, in increasing precedence: baseline defaults,
// answers file, per-field flag overrides.
func assembleConfig(opts generateOpts) (*config.Config, error) {
	cfg := wizard.BuildBaselineConfig()

	if opts.answersPath != "" {
		if err := mergeAnswersFile(cfg, opts.answersPath); err != nil {
			return nil, err
		}
	}

	opts.applyTo(cfg)
	return cfg, nil
}

// mergeAnswersFile reads the partial config at path with strict YAML
// (KnownFields=true) so unknown keys surface as errors. The decode
// merges into cfg in place, so anything not present in the file
// retains the baseline value.
func mergeAnswersFile(cfg *config.Config, path string) (retErr error) {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read answers file: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && retErr == nil {
			retErr = cerr
		}
	}()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && err != io.EOF {
		return fmt.Errorf("parse answers file %s: %w", path, err)
	}
	return nil
}

// applyTo copies non-empty per-field flag values onto cfg. Each
// flag overrides whatever the answers file (or baseline) put there
// -- Helm's `--set` semantics.
func (o generateOpts) applyTo(cfg *config.Config) {
	if o.tunName != "" {
		cfg.TUN.Name = o.tunName
	}
	if o.tunAddress != "" {
		cfg.TUN.Address = o.tunAddress
	}
	if o.tunAddress6 != "" {
		cfg.TUN.Address6 = o.tunAddress6
	}
	if o.socks5Server != "" {
		cfg.SOCKS5.Server = o.socks5Server
	}
	if o.socks5Username != "" {
		cfg.SOCKS5.Username = o.socks5Username
	}
	if o.autoroutesEnabled {
		cfg.AutoRoutes.Enabled = true
	}
}

func printGenerateReport(w io.Writer, r generateReport, asJSON bool) error {
	if asJSON {
		return emitJSON(w, r)
	}
	fmt.Fprintf(w, "Wrote %s (mode %s).\n", r.Path, r.Mode)
	return nil
}
