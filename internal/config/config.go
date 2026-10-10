package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultPath is the canonical location of the Mutiauk config file.
// It is a build-tagged var (see paths_unix.go / paths_windows.go) rather
// than a const because the location differs per OS (/etc on Unix,
// %ProgramData% on Windows) and it is consumed at init time by the
// root command's -c flag default. Centralised so CLI subcommands, the
// service unit generator, and the wizard all reference one value.

// DefaultFileMode is the on-disk mode applied to any config the CLI
// or wizard writes. 0600 because the file carries socks5.password;
// world- or group-readable copies are a credential-disclosure bug
// (HIPAA/PCI auditors flag).
const DefaultFileMode os.FileMode = 0600

// Config is the main configuration structure
type Config struct {
	Daemon     DaemonConfig     `yaml:"daemon"`
	TUN        TUNConfig        `yaml:"tun"`
	SOCKS5     SOCKS5Config     `yaml:"socks5"`
	Routes     []RouteConfig    `yaml:"routes"`
	AutoRoutes AutoRoutesConfig `yaml:"autoroutes"`
	NAT        NATConfig        `yaml:"nat"`
	ICMP       ICMPConfig       `yaml:"icmp"`
	Logging    LoggingConfig    `yaml:"logging"`
	Expose     ExposeConfig     `yaml:"expose"`
	Admin      AdminConfig      `yaml:"admin"`
	// DNSIntegration selects how mesh-host DNS is wired into the host
	// resolver. Valid values: "auto" (default; on Linux resolvectl
	// preferred, fallback to nm-dnsmasq; on macOS /etc/resolver; on Windows
	// NRPT; otherwise none), "resolvectl", "nm-dnsmasq" (Linux),
	// "etc-resolver" (macOS), "nrpt" (Windows), or "none".
	DNSIntegration string `yaml:"dns_integration,omitempty"`
}

// DaemonConfig holds daemon-specific configuration.
//
// StatusProbeTarget is the host:port that `mutiauk status` opens
// a SOCKS5-tunneled TCP connection to in order to verify TCP routing.
// Defaults to "example.com:80" for parity with the legacy hardcoded
// value, but air-gapped or regulated environments (hospitals, DoD)
// will set this to an in-mesh address that is actually reachable
// through the proxy.
type DaemonConfig struct {
	PIDFile    string `yaml:"pid_file"`
	SocketPath string `yaml:"socket_path"`
	// HealthPort is DEPRECATED and unused: no HTTP health port is served.
	// Use the admin HTTP interface (admin.listen) and `mutiauk status`
	// instead. Kept for backward-compatible parsing only.
	HealthPort        int    `yaml:"health_port,omitempty"`
	StatusProbeTarget string `yaml:"status_probe_target"`
}

// TUNConfig holds TUN interface configuration
type TUNConfig struct {
	Name     string `yaml:"name"`
	MTU      int    `yaml:"mtu"`
	Address  string `yaml:"address"`  // IPv4 CIDR, e.g., "10.200.200.1/24"
	Address6 string `yaml:"address6"` // IPv6 CIDR, e.g., "fd00:200::1/64"
}

// SOCKS5Config holds SOCKS5 proxy configuration.
//
// The Password field is overridden by the MUTIAUK_SOCKS5_PASSWORD env
// var when set (env wins over YAML). Use the env var with systemd
// LoadCredential= or k8s secret-mounted env vars so the password
// never has to be written to a config file.
type SOCKS5Config struct {
	Server    string        `yaml:"server"`
	Username  string        `yaml:"username"`
	Password  string        `yaml:"password"`
	Timeout   time.Duration `yaml:"timeout"`
	KeepAlive time.Duration `yaml:"keepalive"`
	Transport string        `yaml:"transport"` // "tcp" (default) or "websocket"
	WSPath    string        `yaml:"ws_path"`   // WebSocket path (default: "/socks5")
}

// RouteConfig holds a single route configuration.
//
// The Enabled field defaults to true when the YAML omits the
// `enabled:` key. Without this default, an operator who writes
//
//   - destination: 10.0.0.0/8
//     comment: "ICU monitor VLAN"
//
// would silently get a disabled route (Go zero-value for bool is
// false). In safety-critical deployments that is a class-action bug,
// so RouteConfig implements UnmarshalYAML to materialise the
// implicit-true default. Explicit `enabled: false` is honoured.
type RouteConfig struct {
	Destination string `yaml:"destination"`
	Comment     string `yaml:"comment"`
	Enabled     bool   `yaml:"enabled"`

	// enabledDefaulted is set to true at unmarshal time when the YAML
	// did not include an explicit `enabled:` key. The daemon emits a
	// one-time warning at startup so operators can audit legacy
	// configs in journald.
	enabledDefaulted bool `yaml:"-"`
}

// UnmarshalYAML implements custom YAML decoding so that a missing
// `enabled:` key defaults to true (instead of Go's bool zero value
// false). Explicit `enabled: false` and `enabled: true` are honoured.
func (r *RouteConfig) UnmarshalYAML(node *yaml.Node) error {
	// Decode into a shadow type with a pointer-bool so we can detect
	// "key absent" vs "key present with value false".
	var raw struct {
		Destination string `yaml:"destination"`
		Comment     string `yaml:"comment"`
		Enabled     *bool  `yaml:"enabled"`
	}
	if err := node.Decode(&raw); err != nil {
		return err
	}
	r.Destination = raw.Destination
	r.Comment = raw.Comment
	if raw.Enabled == nil {
		r.Enabled = true
		r.enabledDefaulted = true
	} else {
		r.Enabled = *raw.Enabled
		r.enabledDefaulted = false
	}
	return nil
}

// EnabledWasDefaulted reports whether the Enabled field on this
// RouteConfig was filled in by the implicit-true default (i.e. the
// YAML omitted `enabled:`). Used by the daemon to surface a one-time
// warning at startup.
func (r RouteConfig) EnabledWasDefaulted() bool { return r.enabledDefaulted }

// ICMPConfig controls how intercepted ICMP echo (ping) traffic is handled.
type ICMPConfig struct {
	// UnreachableReplies controls whether Mutiauk replies to the local
	// sender with an ICMP Destination Unreachable when an echo cannot be
	// forwarded through the mesh (e.g. the exit agent has no unprivileged
	// ICMP socket, or ICMP is disabled on the exit). When false, such
	// failures are dropped silently and the local ping simply times out.
	// A nil pointer (key omitted) defaults to enabled. Note this only fires
	// on forwarding-setup failures; a destination that is reachable but does
	// not answer still times out normally.
	UnreachableReplies *bool `yaml:"unreachable_replies,omitempty"`
}

// EmitUnreachable reports whether ICMP Destination Unreachable replies are
// enabled (the default when unset).
func (c ICMPConfig) EmitUnreachable() bool {
	return c.UnreachableReplies == nil || *c.UnreachableReplies
}

// NATConfig holds NAT table configuration
type NATConfig struct {
	TableSize  int           `yaml:"table_size"`
	TCPTimeout time.Duration `yaml:"tcp_timeout"`
	UDPTimeout time.Duration `yaml:"udp_timeout"`
	GCInterval time.Duration `yaml:"gc_interval"`
}

// LoggingConfig holds logging configuration
type LoggingConfig struct {
	Level      string `yaml:"level"`
	Format     string `yaml:"format"`
	Output     string `yaml:"output"`
	MaxSize    int    `yaml:"max_size"`
	MaxBackups int    `yaml:"max_backups"`
	MaxAge     int    `yaml:"max_age"`
}

// AdminConfig controls the local admin HTTP interface: a browser
// dashboard plus JSON endpoints for inspecting the running daemon
// (applied routes, exposed folders/ports, Muti Metroo agent connection
// status) and managing local exposing.
//
// The interface is bound to loopback only and carries no
// authentication; the loopback binding is the trust boundary, so
// Validate refuses any non-loopback Listen host.
//
// Enabled is a pointer-bool so an omitted `admin:` section (or an
// `admin:` block with no `enabled:` key) defaults to enabled, while an
// explicit `enabled: false` is honoured. Go's bool zero value would
// otherwise silently disable an interface the operator never opted out
// of. Mirrors ICMPConfig.UnreachableReplies. Use AdminEnabled() to
// read the effective value.
type AdminConfig struct {
	Enabled *bool  `yaml:"enabled,omitempty"`
	Listen  string `yaml:"listen"` // loopback host:port, default 127.0.0.1:48088
}

// AdminEnabled reports whether the admin HTTP interface is enabled (the
// default when unset).
func (c AdminConfig) AdminEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// ExposeConfig declares forwards published from this Mutiauk to
// the upstream Muti Metroo agent over the SOCKS5 endpoint (socks5.server)
// via the CmdExpose stream. Authorization is the SOCKS5 handshake; no
// separate URL or token.
//
// MaxStreamsPerKey caps the number of concurrent inbound streams the
// session will accept for any single routing key. Default 256. A
// misbehaving mesh peer that opens streams faster than they close
// would otherwise exhaust file descriptors on this host. The 257th
// concurrent OPEN for a given key is refused with an OPEN_ACK error;
// existing streams are unaffected.
//
// MaxBufferedBytes caps the per-stream receive buffer (the protocol
// has no flow control, so this is the only backpressure). Default
// 16 MiB. Hitting the cap closes the stream cleanly. Lower this on
// resource-constrained hosts.
type ExposeConfig struct {
	Enabled          bool            `yaml:"enabled"`
	ReconnectMin     time.Duration   `yaml:"reconnect_min"`       // default 1s
	ReconnectMax     time.Duration   `yaml:"reconnect_max"`       // default 30s
	KeepalivePeriod  time.Duration   `yaml:"keepalive_period"`    // default 25s
	MaxStreamsPerKey int             `yaml:"max_streams_per_key"` // default 256
	MaxBufferedBytes int             `yaml:"max_buffered_bytes"`  // default 16<<20 (16 MiB)
	Forwards         []ForwardConfig `yaml:"forwards"`
}

// Forward type values written in YAML and matched in validation /
// expose handler dispatch. Stable strings — changing them is a
// config-format breaking change.
const (
	ForwardTypeFolder = "folder"
	ForwardTypeTCP    = "tcp"
)

// ForwardConfig is one routing key published over the expose stream.
// Type ForwardTypeFolder serves files from Path (strict: 404 for
// anything that is not an exact file). Type ForwardTypeTCP dials
// Target and pipes bytes.
type ForwardConfig struct {
	Key     string `yaml:"key"`
	Type    string `yaml:"type"`
	Path    string `yaml:"path,omitempty"`
	Target  string `yaml:"target,omitempty"`
	Comment string `yaml:"comment,omitempty"`
}

// AutoRoutesConfig holds automatic route fetching configuration. Route/host
// state always rides the SOCKS5 endpoint (socks5.server) route-watch stream;
// the only knob is whether the feature is enabled.
type AutoRoutesConfig struct {
	Enabled bool `yaml:"enabled"`
}

// Load reads configuration from a YAML file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	cfg.setDefaults()

	// Env overrides win over YAML for secrets, so deployments using
	// systemd LoadCredential or k8s secret mounts never need to write
	// the secret to /etc/mutiauk/config.yaml.
	if pw := os.Getenv("MUTIAUK_SOCKS5_PASSWORD"); pw != "" {
		cfg.SOCKS5.Password = pw
	}

	return cfg, nil
}

// ParseRaw unmarshals configuration from YAML bytes into an empty
// Config WITHOUT applying defaults and WITHOUT the env-secret
// overrides that Load folds in. Every field the operator did not set
// is left at its zero value, which lets callers tell "explicitly set"
// apart from "defaulted". The setup wizard uses this to preserve only
// the sections an operator actually configured when re-running on an
// existing config, instead of re-emitting every default value (and to
// keep MUTIAUK_SOCKS5_PASSWORD out of the written file).
func ParseRaw(data []byte) (*Config, error) {
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	return cfg, nil
}

// LegacyDNSBlockPresent reports whether a legacy `dns:` block is
// present in the YAML at path. Used by the daemon to log a one-time
// deprecation warning; the block itself is silently ignored at parse
// time (DNS now follows from autoroutes and the agent's
// mesh_hosts_suffixes).
func LegacyDNSBlockPresent(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var probe map[string]any
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return false
	}
	_, ok := probe["dns"]
	return ok
}

// ApplyDefaults populates unset fields with their defaults. Load
// calls it automatically; callers that build a Config in code (the
// non-interactive setup path, tests) should invoke it before
// Validate so defaulted-only fields like daemon.status_probe_target
// don't surface as bogus validation errors.
func (c *Config) ApplyDefaults() { c.setDefaults() }

// setDefaults applies default values to unset fields
func (c *Config) setDefaults() {
	// Daemon defaults (paths are per-OS; see paths_{unix,windows}.go)
	if c.Daemon.PIDFile == "" {
		c.Daemon.PIDFile = defaultPIDFile
	}
	if c.Daemon.SocketPath == "" {
		c.Daemon.SocketPath = defaultSocketPath
	}
	if c.Daemon.StatusProbeTarget == "" {
		c.Daemon.StatusProbeTarget = "example.com:80"
	}

	// TUN defaults
	if c.TUN.Name == "" {
		c.TUN.Name = "tun0"
	}
	if c.TUN.MTU == 0 {
		c.TUN.MTU = 1400
	}
	if c.TUN.Address == "" {
		c.TUN.Address = "10.200.200.1/24"
	}

	// SOCKS5 defaults
	if c.SOCKS5.Server == "" {
		c.SOCKS5.Server = "127.0.0.1:1080"
	}
	if c.SOCKS5.Timeout == 0 {
		c.SOCKS5.Timeout = 30 * time.Second
	}
	if c.SOCKS5.KeepAlive == 0 {
		c.SOCKS5.KeepAlive = 60 * time.Second
	}
	if c.SOCKS5.Transport == "" {
		c.SOCKS5.Transport = "tcp"
	}
	if c.SOCKS5.WSPath == "" {
		c.SOCKS5.WSPath = "/socks5"
	}

	// ICMP defaults
	if c.ICMP.UnreachableReplies == nil {
		enabled := true
		c.ICMP.UnreachableReplies = &enabled
	}

	// NAT defaults
	if c.NAT.TableSize == 0 {
		c.NAT.TableSize = 65536
	}
	if c.NAT.TCPTimeout == 0 {
		c.NAT.TCPTimeout = time.Hour
	}
	if c.NAT.UDPTimeout == 0 {
		c.NAT.UDPTimeout = 5 * time.Minute
	}
	if c.NAT.GCInterval == 0 {
		c.NAT.GCInterval = time.Minute
	}

	// Logging defaults
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "json"
	}

	// DNS integration defaults
	if c.DNSIntegration == "" {
		c.DNSIntegration = "auto"
	}

	// Expose defaults
	if c.Expose.ReconnectMin == 0 {
		c.Expose.ReconnectMin = time.Second
	}
	if c.Expose.ReconnectMax == 0 {
		c.Expose.ReconnectMax = 30 * time.Second
	}
	if c.Expose.KeepalivePeriod == 0 {
		c.Expose.KeepalivePeriod = 25 * time.Second
	}
	if c.Expose.MaxStreamsPerKey == 0 {
		c.Expose.MaxStreamsPerKey = 256
	}
	if c.Expose.MaxBufferedBytes == 0 {
		c.Expose.MaxBufferedBytes = 16 << 20 // 16 MiB
	}

	// Admin HTTP defaults (enabled-by-default, loopback-only)
	if c.Admin.Enabled == nil {
		enabled := true
		c.Admin.Enabled = &enabled
	}
	if c.Admin.Listen == "" {
		c.Admin.Listen = "127.0.0.1:48088"
	}

	// Per-route Enabled default (true when the YAML omitted the key)
	// is materialised by RouteConfig.UnmarshalYAML, so nothing needs
	// to happen here.
}

// Validate checks the configuration for errors
func (c *Config) Validate() error {
	// Validate daemon.status_probe_target. Empty means "use default"
	// (set by setDefaults), so by the time we reach Validate it is
	// always populated; we still split the host:port to catch typos.
	if host, port, err := net.SplitHostPort(c.Daemon.StatusProbeTarget); err != nil {
		return fmt.Errorf("invalid daemon.status_probe_target: %w", err)
	} else if host == "" || port == "" {
		return fmt.Errorf("daemon.status_probe_target must be host:port (got %q)", c.Daemon.StatusProbeTarget)
	}

	// Validate TUN config
	if c.TUN.Name == "" {
		return fmt.Errorf("tun.name is required")
	}
	if c.TUN.MTU < 576 || c.TUN.MTU > 65535 {
		return fmt.Errorf("tun.mtu must be between 576 and 65535")
	}

	// Validate TUN IPv4 address
	if c.TUN.Address != "" {
		ip, ipNet, err := net.ParseCIDR(c.TUN.Address)
		if err != nil {
			return fmt.Errorf("invalid tun.address: %w", err)
		}
		if ip.To4() == nil {
			return fmt.Errorf("tun.address must be an IPv4 address")
		}
		_ = ipNet // used for validation
	}

	// Validate TUN IPv6 address
	if c.TUN.Address6 != "" {
		ip, ipNet, err := net.ParseCIDR(c.TUN.Address6)
		if err != nil {
			return fmt.Errorf("invalid tun.address6: %w", err)
		}
		if ip.To4() != nil {
			return fmt.Errorf("tun.address6 must be an IPv6 address")
		}
		_ = ipNet // used for validation
	}

	// Validate SOCKS5 config
	if c.SOCKS5.Server == "" {
		return fmt.Errorf("socks5.server is required")
	}

	// Check if server is a WebSocket URL
	if strings.HasPrefix(c.SOCKS5.Server, "ws://") || strings.HasPrefix(c.SOCKS5.Server, "wss://") {
		u, err := url.Parse(c.SOCKS5.Server)
		if err != nil {
			return fmt.Errorf("invalid socks5.server URL: %w", err)
		}
		if u.Host == "" {
			return fmt.Errorf("socks5.server URL must include host")
		}
	} else {
		// Traditional host:port format
		host, port, err := net.SplitHostPort(c.SOCKS5.Server)
		if err != nil {
			return fmt.Errorf("invalid socks5.server format: %w", err)
		}
		if host == "" || port == "" {
			return fmt.Errorf("socks5.server must include host and port")
		}
	}

	// Validate transport
	if c.SOCKS5.Transport != "tcp" && c.SOCKS5.Transport != "websocket" {
		return fmt.Errorf("socks5.transport must be 'tcp' or 'websocket'")
	}

	// Validate ws_path
	if c.SOCKS5.WSPath != "" && !strings.HasPrefix(c.SOCKS5.WSPath, "/") {
		return fmt.Errorf("socks5.ws_path must start with '/'")
	}

	// Validate routes
	for i, r := range c.Routes {
		if r.Destination == "" {
			return fmt.Errorf("routes[%d].destination is required", i)
		}
		_, _, err := net.ParseCIDR(r.Destination)
		if err != nil {
			return fmt.Errorf("invalid routes[%d].destination: %w", i, err)
		}
	}

	// autoroutes needs no validation: enabled is a bool, and route state
	// always rides the SOCKS5 endpoint validated above.

	// Validate dns_integration. etc-resolver (macOS) and nrpt (Windows) are
	// real backends that DetectIntegration also selects under "auto"; accept
	// them explicitly so a platform-specific config does not fail validation.
	switch c.DNSIntegration {
	case "auto", "resolvectl", "nm-dnsmasq", "etc-resolver", "nrpt", "none":
	default:
		return fmt.Errorf("invalid dns_integration %q (want auto, resolvectl, nm-dnsmasq, etc-resolver, nrpt, or none)", c.DNSIntegration)
	}

	// Validate admin HTTP interface. Loopback-only is a hard guarantee:
	// the interface is unauthenticated, so binding it to a routable
	// address would expose daemon control to the network.
	if c.Admin.AdminEnabled() {
		host, port, err := net.SplitHostPort(c.Admin.Listen)
		if err != nil {
			return fmt.Errorf("invalid admin.listen: %w", err)
		}
		if host == "" || port == "" {
			return fmt.Errorf("admin.listen must be host:port (got %q)", c.Admin.Listen)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("admin.listen host %q must be a loopback IP address (e.g. 127.0.0.1 or ::1)", host)
		}
		if !ip.IsLoopback() {
			return fmt.Errorf("admin.listen host %q must be a loopback address; the admin interface is unauthenticated and must not be reachable externally", host)
		}
	}

	if err := c.validateExpose(); err != nil {
		return err
	}

	return nil
}

// keyPattern is the allowed character set for a forward routing key.
// Matches what the Muti Metroo server accepts; keep it conservative so the
// key can appear in URLs and log lines without escaping.
var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func (c *Config) validateExpose() error {
	if !c.Expose.Enabled {
		return nil
	}
	if c.SOCKS5.Server == "" {
		return fmt.Errorf("expose requires socks5.server (the expose session runs over the SOCKS5 endpoint)")
	}
	if c.Expose.ReconnectMin < 100*time.Millisecond {
		return fmt.Errorf("expose.reconnect_min must be at least 100ms")
	}
	if c.Expose.ReconnectMax < c.Expose.ReconnectMin {
		return fmt.Errorf("expose.reconnect_max must be >= expose.reconnect_min")
	}
	if c.Expose.KeepalivePeriod < time.Second {
		return fmt.Errorf("expose.keepalive_period must be at least 1s")
	}
	if c.Expose.MaxStreamsPerKey < 1 {
		return fmt.Errorf("expose.max_streams_per_key must be at least 1")
	}
	if c.Expose.MaxBufferedBytes < 64<<10 {
		return fmt.Errorf("expose.max_buffered_bytes must be at least 65536 (64 KiB)")
	}

	seen := make(map[string]struct{}, len(c.Expose.Forwards))
	for i, f := range c.Expose.Forwards {
		if err := validateForward(f, i); err != nil {
			return err
		}
		if _, dup := seen[f.Key]; dup {
			return fmt.Errorf("expose.forwards[%d]: duplicate key %q", i, f.Key)
		}
		seen[f.Key] = struct{}{}
	}
	return nil
}

// ValidateForward applies the same per-entry validation used at config
// load time. Exposed for runtime add/remove via the API server.
func ValidateForward(f ForwardConfig) error { return validateForward(f, -1) }

func validateForward(f ForwardConfig, idx int) error {
	prefix := "expose.forwards"
	if idx >= 0 {
		prefix = fmt.Sprintf("expose.forwards[%d]", idx)
	}
	if f.Key == "" {
		return fmt.Errorf("%s: key is required", prefix)
	}
	if !keyPattern.MatchString(f.Key) {
		return fmt.Errorf("%s: key %q must match [a-zA-Z0-9._-]+", prefix, f.Key)
	}
	switch f.Type {
	case ForwardTypeFolder:
		if f.Path == "" {
			return fmt.Errorf("%s: path is required for type=%s", prefix, ForwardTypeFolder)
		}
		if !filepath.IsAbs(f.Path) {
			return fmt.Errorf("%s: path %q must be absolute", prefix, f.Path)
		}
		info, err := os.Stat(f.Path)
		if err != nil {
			return fmt.Errorf("%s: path %q: %w", prefix, f.Path, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s: path %q is not a directory", prefix, f.Path)
		}
		if f.Target != "" {
			return fmt.Errorf("%s: target must be empty for type=%s", prefix, ForwardTypeFolder)
		}
	case ForwardTypeTCP:
		if f.Target == "" {
			return fmt.Errorf("%s: target is required for type=%s", prefix, ForwardTypeTCP)
		}
		host, port, err := net.SplitHostPort(f.Target)
		if err != nil {
			return fmt.Errorf("%s: invalid target %q: %w", prefix, f.Target, err)
		}
		if host == "" || port == "" {
			return fmt.Errorf("%s: target %q must include host and port", prefix, f.Target)
		}
		if f.Path != "" {
			return fmt.Errorf("%s: path must be empty for type=%s", prefix, ForwardTypeTCP)
		}
	default:
		return fmt.Errorf("%s: type must be %q or %q (got %q)", prefix, ForwardTypeFolder, ForwardTypeTCP, f.Type)
	}
	return nil
}

// GetTUNIPv4 returns the parsed TUN IPv4 address and network
func (c *Config) GetTUNIPv4() (net.IP, *net.IPNet, error) {
	if c.TUN.Address == "" {
		return nil, nil, nil
	}
	return net.ParseCIDR(c.TUN.Address)
}

// GetTUNIPv6 returns the parsed TUN IPv6 address and network
func (c *Config) GetTUNIPv6() (net.IP, *net.IPNet, error) {
	if c.TUN.Address6 == "" {
		return nil, nil, nil
	}
	return net.ParseCIDR(c.TUN.Address6)
}

// HasAuth returns true if SOCKS5 authentication is configured
func (c *Config) HasAuth() bool {
	return c.SOCKS5.Username != "" && c.SOCKS5.Password != ""
}

// Save writes the configuration to a YAML file with mode 0600.
//
// The 0600 mode matters: the config holds the SOCKS5 password, the
// Muti Metroo bearer token, and other secrets. World- or group-readable
// configs are a credential-disclosure bug (HIPAA/PCI auditors flag).
//
// Save also tightens the permission of an existing file at `path`,
// since `os.WriteFile` only sets the mode when creating the file.
func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, DefaultFileMode); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}
	if err := os.Chmod(path, DefaultFileMode); err != nil {
		return fmt.Errorf("failed to chmod config file: %w", err)
	}
	return nil
}

// CheckFileMode returns a non-empty warning string when the config
// file at `path` is group- or world-readable, and an empty string
// otherwise. Callers (daemon start, config check) log the warning
// but do not fail; the file is still usable.
func CheckFileMode(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	mode := info.Mode().Perm()
	// 0600 = -rw------- ; anything with bits in the bottom six is too permissive
	if mode&0o077 != 0 {
		return fmt.Sprintf(
			"config file %s has mode %#o; expected 0600 (contains secrets like socks5.password). Run: sudo chmod 600 %s",
			path, mode, path,
		)
	}
	return ""
}
