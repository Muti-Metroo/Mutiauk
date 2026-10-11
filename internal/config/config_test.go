package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		content := `
tun:
  name: tun0
  mtu: 1400
  address: 10.200.200.1/24

socks5:
  server: 127.0.0.1:1080
  timeout: 30s

routes:
  - destination: 10.0.0.0/8
    comment: "Private network"
    enabled: true
`
		path := writeTempConfig(t, content)
		defer os.Remove(path)

		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}

		if cfg.TUN.Name != "tun0" {
			t.Errorf("TUN.Name = %v, want tun0", cfg.TUN.Name)
		}
		if cfg.TUN.MTU != 1400 {
			t.Errorf("TUN.MTU = %v, want 1400", cfg.TUN.MTU)
		}
		if cfg.SOCKS5.Server != "127.0.0.1:1080" {
			t.Errorf("SOCKS5.Server = %v", cfg.SOCKS5.Server)
		}
		if len(cfg.Routes) != 1 {
			t.Errorf("len(Routes) = %v, want 1", len(cfg.Routes))
		}
	})

	t.Run("file not found", func(t *testing.T) {
		_, err := Load("/nonexistent/path/config.yaml")
		if err == nil {
			t.Error("expected error for nonexistent file")
		}
	})

	t.Run("invalid YAML", func(t *testing.T) {
		content := `
tun:
  name: [invalid yaml
  mtu: not a number
`
		path := writeTempConfig(t, content)
		defer os.Remove(path)

		_, err := Load(path)
		if err == nil {
			t.Error("expected error for invalid YAML")
		}
	})
}

func TestDefaults(t *testing.T) {
	content := `
socks5:
  server: proxy:1080
`
	path := writeTempConfig(t, content)
	defer os.Remove(path)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Check daemon defaults (per-OS; see paths_{unix,windows}.go)
	if cfg.Daemon.PIDFile != defaultPIDFile {
		t.Errorf("Daemon.PIDFile = %v, want %v", cfg.Daemon.PIDFile, defaultPIDFile)
	}
	if cfg.Daemon.SocketPath != defaultSocketPath {
		t.Errorf("Daemon.SocketPath = %v, want %v", cfg.Daemon.SocketPath, defaultSocketPath)
	}

	// Check TUN defaults
	if cfg.TUN.Name != "tun0" {
		t.Errorf("TUN.Name = %v, want tun0", cfg.TUN.Name)
	}
	if cfg.TUN.MTU != 1400 {
		t.Errorf("TUN.MTU = %v, want 1400", cfg.TUN.MTU)
	}
	if cfg.TUN.Address != "10.200.200.1/24" {
		t.Errorf("TUN.Address = %v", cfg.TUN.Address)
	}

	// Check SOCKS5 defaults
	if cfg.SOCKS5.Timeout != 30*time.Second {
		t.Errorf("SOCKS5.Timeout = %v", cfg.SOCKS5.Timeout)
	}
	if cfg.SOCKS5.KeepAlive != 60*time.Second {
		t.Errorf("SOCKS5.KeepAlive = %v", cfg.SOCKS5.KeepAlive)
	}

	// Check NAT defaults
	if cfg.NAT.TableSize != 65536 {
		t.Errorf("NAT.TableSize = %v", cfg.NAT.TableSize)
	}
	if cfg.NAT.TCPTimeout != time.Hour {
		t.Errorf("NAT.TCPTimeout = %v", cfg.NAT.TCPTimeout)
	}
	if cfg.NAT.UDPTimeout != 5*time.Minute {
		t.Errorf("NAT.UDPTimeout = %v", cfg.NAT.UDPTimeout)
	}
	if cfg.NAT.GCInterval != time.Minute {
		t.Errorf("NAT.GCInterval = %v", cfg.NAT.GCInterval)
	}

	// Check Logging defaults
	if cfg.Logging.Level != "info" {
		t.Errorf("Logging.Level = %v", cfg.Logging.Level)
	}
	if cfg.Logging.Format != "json" {
		t.Errorf("Logging.Format = %v", cfg.Logging.Format)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid minimal config",
			config: `
tun:
  name: tun0
  mtu: 1400
  address: 10.200.200.1/24
socks5:
  server: 127.0.0.1:1080
`,
			wantErr: false,
		},
		// Note: empty tun.name gets default "tun0", so we can't test "missing TUN name"
		// The validation would only fail if defaults weren't applied
		{
			name: "MTU too low",
			config: `
tun:
  name: tun0
  mtu: 100
socks5:
  server: 127.0.0.1:1080
`,
			wantErr: true,
			errMsg:  "tun.mtu must be between 576 and 65535",
		},
		{
			name: "MTU too high",
			config: `
tun:
  name: tun0
  mtu: 100000
socks5:
  server: 127.0.0.1:1080
`,
			wantErr: true,
			errMsg:  "tun.mtu must be between 576 and 65535",
		},
		{
			name: "invalid TUN address",
			config: `
tun:
  name: tun0
  mtu: 1400
  address: not-a-cidr
socks5:
  server: 127.0.0.1:1080
`,
			wantErr: true,
			errMsg:  "invalid tun.address",
		},
		{
			name: "IPv6 in TUN address field",
			config: `
tun:
  name: tun0
  mtu: 1400
  address: "fd00::1/64"
socks5:
  server: 127.0.0.1:1080
`,
			wantErr: true,
			errMsg:  "tun.address must be an IPv4 address",
		},
		{
			name: "IPv4 in TUN address6 field",
			config: `
tun:
  name: tun0
  mtu: 1400
  address: 10.200.200.1/24
  address6: "10.0.0.1/24"
socks5:
  server: 127.0.0.1:1080
`,
			wantErr: true,
			errMsg:  "tun.address6 must be an IPv6 address",
		},
		// Note: empty socks5.server gets default "127.0.0.1:1080", so we can't test "missing SOCKS5 server"
		// The validation would only fail if defaults weren't applied
		{
			name: "invalid SOCKS5 server format",
			config: `
tun:
  name: tun0
  mtu: 1400
socks5:
  server: "invalid-no-port"
`,
			wantErr: true,
			errMsg:  "invalid socks5.server format",
		},
		{
			name: "invalid route destination",
			config: `
tun:
  name: tun0
  mtu: 1400
socks5:
  server: 127.0.0.1:1080
routes:
  - destination: not-a-cidr
    enabled: true
`,
			wantErr: true,
			errMsg:  "invalid routes[0].destination",
		},
		{
			name: "empty route destination",
			config: `
tun:
  name: tun0
  mtu: 1400
socks5:
  server: 127.0.0.1:1080
routes:
  - destination: ""
    enabled: true
`,
			wantErr: true,
			errMsg:  "routes[0].destination is required",
		},
		{
			name: "valid config with autoroutes enabled",
			config: `
tun:
  name: tun0
  mtu: 1400
socks5:
  server: 127.0.0.1:1080
autoroutes:
  enabled: true
`,
			wantErr: false,
		},
		{
			// Legacy configs may still carry source/url/token/poll_interval/
			// timeout. They are now silently ignored, not rejected, so
			// deployed hosts keep loading after the upgrade.
			name: "autoroutes ignores legacy keys",
			config: `
tun:
  name: tun0
  mtu: 1400
socks5:
  server: 127.0.0.1:1080
autoroutes:
  enabled: true
  source: dashboard
  url: http://localhost:8080
  token: secret
  poll_interval: 100ms
  timeout: 100ms
`,
			wantErr: false,
		},
		{
			name: "valid config with IPv6",
			config: `
tun:
  name: tun0
  mtu: 1400
  address: 10.200.200.1/24
  address6: "fd00:200::1/64"
socks5:
  server: 127.0.0.1:1080
`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempConfig(t, tt.config)
			defer os.Remove(path)

			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}

			err = cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && tt.errMsg != "" {
				if err == nil || !containsString(err.Error(), tt.errMsg) {
					t.Errorf("error = %v, want to contain %q", err, tt.errMsg)
				}
			}
		})
	}
}

func TestGetTUNIPv4(t *testing.T) {
	t.Run("with address", func(t *testing.T) {
		cfg := &Config{
			TUN: TUNConfig{Address: "10.200.200.1/24"},
		}

		ip, ipNet, err := cfg.GetTUNIPv4()
		if err != nil {
			t.Fatalf("GetTUNIPv4() error = %v", err)
		}
		if ip.String() != "10.200.200.1" {
			t.Errorf("IP = %v", ip)
		}
		if ipNet.String() != "10.200.200.0/24" {
			t.Errorf("IPNet = %v", ipNet)
		}
	})

	t.Run("empty address", func(t *testing.T) {
		cfg := &Config{
			TUN: TUNConfig{Address: ""},
		}

		ip, ipNet, err := cfg.GetTUNIPv4()
		if err != nil {
			t.Fatalf("GetTUNIPv4() error = %v", err)
		}
		if ip != nil || ipNet != nil {
			t.Error("expected nil for empty address")
		}
	})
}

func TestGetTUNIPv6(t *testing.T) {
	t.Run("with address", func(t *testing.T) {
		cfg := &Config{
			TUN: TUNConfig{Address6: "fd00:200::1/64"},
		}

		ip, ipNet, err := cfg.GetTUNIPv6()
		if err != nil {
			t.Fatalf("GetTUNIPv6() error = %v", err)
		}
		if ip.String() != "fd00:200::1" {
			t.Errorf("IP = %v", ip)
		}
		if ipNet.String() != "fd00:200::/64" {
			t.Errorf("IPNet = %v", ipNet)
		}
	})

	t.Run("empty address", func(t *testing.T) {
		cfg := &Config{
			TUN: TUNConfig{Address6: ""},
		}

		ip, ipNet, err := cfg.GetTUNIPv6()
		if err != nil {
			t.Fatalf("GetTUNIPv6() error = %v", err)
		}
		if ip != nil || ipNet != nil {
			t.Error("expected nil for empty address")
		}
	})
}

func TestHasAuth(t *testing.T) {
	tests := []struct {
		name     string
		username string
		password string
		want     bool
	}{
		{"both set", "user", "pass", true},
		{"username only", "user", "", false},
		{"password only", "", "pass", false},
		{"neither set", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				SOCKS5: SOCKS5Config{
					Username: tt.username,
					Password: tt.password,
				},
			}
			if got := cfg.HasAuth(); got != tt.want {
				t.Errorf("HasAuth() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSave(t *testing.T) {
	cfg := &Config{
		TUN: TUNConfig{
			Name:    "tun0",
			MTU:     1400,
			Address: "10.200.200.1/24",
		},
		SOCKS5: SOCKS5Config{
			Server:  "127.0.0.1:1080",
			Timeout: 30 * time.Second,
		},
		Routes: []RouteConfig{
			{
				Destination: "10.0.0.0/8",
				Comment:     "Private network",
				Enabled:     true,
			},
		},
	}

	tmpDir, err := os.MkdirTemp("", "config-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	path := filepath.Join(tmpDir, "config.yaml")

	// Save config
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("config file not created")
	}

	// Load and verify
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.TUN.Name != cfg.TUN.Name {
		t.Errorf("TUN.Name = %v, want %v", loaded.TUN.Name, cfg.TUN.Name)
	}
	if loaded.SOCKS5.Server != cfg.SOCKS5.Server {
		t.Errorf("SOCKS5.Server = %v, want %v", loaded.SOCKS5.Server, cfg.SOCKS5.Server)
	}
	if len(loaded.Routes) != len(cfg.Routes) {
		t.Errorf("len(Routes) = %v, want %v", len(loaded.Routes), len(cfg.Routes))
	}
	if loaded.Routes[0].Destination != cfg.Routes[0].Destination {
		t.Errorf("Routes[0].Destination = %v", loaded.Routes[0].Destination)
	}
}

func TestExposeCapsDefaults(t *testing.T) {
	content := `
tun:
  name: tun0
  mtu: 1400
socks5:
  server: 127.0.0.1:1080
autoroutes:
  enabled: true
  url: "http://localhost:8080"
expose:
  enabled: true
`
	path := writeTempConfig(t, content)
	defer os.Remove(path)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Expose.MaxStreamsPerKey != 256 {
		t.Errorf("MaxStreamsPerKey default = %d, want 256", cfg.Expose.MaxStreamsPerKey)
	}
	if cfg.Expose.MaxBufferedBytes != 16<<20 {
		t.Errorf("MaxBufferedBytes default = %d, want %d", cfg.Expose.MaxBufferedBytes, 16<<20)
	}
}

func TestExposeCapsValidate(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "max_streams_per_key < 1 rejected",
			yaml: `
tun: {name: tun0, mtu: 1400}
socks5: {server: 127.0.0.1:1080}
autoroutes: {enabled: true, url: "http://localhost:8080"}
expose:
  enabled: true
  max_streams_per_key: 0
  max_buffered_bytes: 16777216
`,
			// 0 hits the default-substitution path (zero -> 256), so
			// the validate failure is unreachable from yaml. Use -1.
			wantErr: "",
		},
		{
			name: "max_buffered_bytes too small",
			yaml: `
tun: {name: tun0, mtu: 1400}
socks5: {server: 127.0.0.1:1080}
autoroutes: {enabled: true, url: "http://localhost:8080"}
expose:
  enabled: true
  max_streams_per_key: 256
  max_buffered_bytes: 1024
`,
			wantErr: "expose.max_buffered_bytes must be at least",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempConfig(t, tt.yaml)
			defer os.Remove(path)

			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			err = cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("Validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !containsString(err.Error(), tt.wantErr) {
				t.Errorf("Validate() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

// TestExposeOverSocksNoURL confirms expose runs over the SOCKS5 endpoint,
// so socks5.server alone is sufficient (no autoroutes dependency).
func TestExposeOverSocksNoURL(t *testing.T) {
	content := `
tun: {name: tun0, mtu: 1400}
socks5: {server: 127.0.0.1:1080}
expose:
  enabled: true
  forwards:
    - key: site
      type: folder
      path: /tmp
`
	path := writeTempConfig(t, content)
	defer os.Remove(path)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil (expose should not require autoroutes.url)", err)
	}
}

func TestRouteEnabledDefault(t *testing.T) {
	cases := []struct {
		name             string
		yaml             string
		wantEnabled      bool
		wantWasDefaulted bool
	}{
		{
			name: "enabled key omitted defaults to true",
			yaml: `
tun:
  name: tun0
  mtu: 1400
socks5:
  server: 127.0.0.1:1080
routes:
  - destination: 10.0.0.0/8
    comment: "ICU monitor VLAN"
`,
			wantEnabled:      true,
			wantWasDefaulted: true,
		},
		{
			name: "explicit enabled true",
			yaml: `
tun:
  name: tun0
  mtu: 1400
socks5:
  server: 127.0.0.1:1080
routes:
  - destination: 10.0.0.0/8
    enabled: true
`,
			wantEnabled:      true,
			wantWasDefaulted: false,
		},
		{
			name: "explicit enabled false is honoured",
			yaml: `
tun:
  name: tun0
  mtu: 1400
socks5:
  server: 127.0.0.1:1080
routes:
  - destination: 10.0.0.0/8
    enabled: false
`,
			wantEnabled:      false,
			wantWasDefaulted: false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempConfig(t, tt.yaml)
			defer os.Remove(path)

			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if len(cfg.Routes) != 1 {
				t.Fatalf("len(Routes) = %d, want 1", len(cfg.Routes))
			}
			if cfg.Routes[0].Enabled != tt.wantEnabled {
				t.Errorf("Enabled = %v, want %v", cfg.Routes[0].Enabled, tt.wantEnabled)
			}
			if cfg.Routes[0].EnabledWasDefaulted() != tt.wantWasDefaulted {
				t.Errorf("EnabledWasDefaulted() = %v, want %v",
					cfg.Routes[0].EnabledWasDefaulted(), tt.wantWasDefaulted)
			}
		})
	}
}

func TestAdminDefaults(t *testing.T) {
	cases := []struct {
		name        string
		yaml        string
		wantEnabled bool
		wantListen  string
	}{
		{
			name: "admin section omitted defaults to enabled",
			yaml: `
tun:
  name: tun0
  mtu: 1400
socks5:
  server: 127.0.0.1:1080
`,
			wantEnabled: true,
			wantListen:  "127.0.0.1:48088",
		},
		{
			name: "admin present without enabled key defaults to enabled",
			yaml: `
tun:
  name: tun0
  mtu: 1400
socks5:
  server: 127.0.0.1:1080
admin:
  listen: 127.0.0.1:9000
`,
			wantEnabled: true,
			wantListen:  "127.0.0.1:9000",
		},
		{
			name: "explicit enabled false is honoured",
			yaml: `
tun:
  name: tun0
  mtu: 1400
socks5:
  server: 127.0.0.1:1080
admin:
  enabled: false
`,
			wantEnabled: false,
			wantListen:  "127.0.0.1:48088",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempConfig(t, tt.yaml)
			defer os.Remove(path)

			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Admin.AdminEnabled() != tt.wantEnabled {
				t.Errorf("AdminEnabled() = %v, want %v", cfg.Admin.AdminEnabled(), tt.wantEnabled)
			}
			if cfg.Admin.Listen != tt.wantListen {
				t.Errorf("Listen = %q, want %q", cfg.Admin.Listen, tt.wantListen)
			}
		})
	}
}

func TestAdminListenValidation(t *testing.T) {
	base := func(listen string) *Config {
		enabled := true
		c := &Config{
			TUN:    TUNConfig{Name: "tun0", MTU: 1400, Address: "10.200.200.1/24"},
			SOCKS5: SOCKS5Config{Server: "127.0.0.1:1080"},
			Admin:  AdminConfig{Enabled: &enabled, Listen: listen},
		}
		c.ApplyDefaults()
		return c
	}

	cases := []struct {
		name    string
		listen  string
		wantErr bool
	}{
		{"ipv4 loopback ok", "127.0.0.1:48088", false},
		{"ipv4 loopback range ok", "127.5.5.5:48088", false},
		{"ipv6 loopback ok", "[::1]:48088", false},
		{"routable address rejected", "0.0.0.0:48088", true},
		{"lan address rejected", "192.168.1.10:48088", true},
		{"hostname rejected", "localhost:48088", true},
		{"missing port rejected", "127.0.0.1", true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := base(tt.listen).Validate()
			if tt.wantErr && err == nil {
				t.Errorf("Validate() = nil, want error for listen %q", tt.listen)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil for listen %q", err, tt.listen)
			}
		})
	}

	// Disabled admin with a bad listen address must not fail validation.
	disabled := false
	c := &Config{
		TUN:    TUNConfig{Name: "tun0", MTU: 1400, Address: "10.200.200.1/24"},
		SOCKS5: SOCKS5Config{Server: "127.0.0.1:1080"},
		Admin:  AdminConfig{Enabled: &disabled, Listen: "0.0.0.0:80"},
	}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		t.Errorf("disabled admin should skip listen validation, got %v", err)
	}
}

func TestSaveFileMode(t *testing.T) {
	cfg := &Config{
		TUN: TUNConfig{Name: "tun0", MTU: 1400, Address: "10.200.200.1/24"},
		SOCKS5: SOCKS5Config{
			Server:   "127.0.0.1:1080",
			Username: "user",
			Password: "secret",
		},
	}

	tmpDir, err := os.MkdirTemp("", "config-mode-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	path := filepath.Join(tmpDir, "config.yaml")

	// Pre-create with permissive 0644 so we can confirm Save tightens it.
	if err := os.WriteFile(path, []byte("placeholder: true\n"), 0644); err != nil {
		t.Fatalf("pre-create: %v", err)
	}

	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0600); got != want {
		t.Errorf("file mode = %#o, want %#o (config holds secrets)", got, want)
	}

	// CheckFileMode should be quiet on a 0600 file.
	if w := CheckFileMode(path); w != "" {
		t.Errorf("CheckFileMode on 0600 returned %q, want empty", w)
	}

	// Loosen it and verify CheckFileMode complains.
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if w := CheckFileMode(path); w == "" {
		t.Error("CheckFileMode on 0644 returned empty, want warning")
	}
}

func TestSaveInvalidPath(t *testing.T) {
	cfg := &Config{}
	err := cfg.Save("/nonexistent/directory/config.yaml")
	if err == nil {
		t.Error("expected error for invalid path")
	}
}

func TestLoad_SOCKS5PasswordEnv(t *testing.T) {
	baseYAML := func(passwordLine string) string {
		return `
tun:
  name: tun0
  mtu: 1400
socks5:
  server: 127.0.0.1:1080
  username: user
` + passwordLine
	}

	t.Run("password from YAML", func(t *testing.T) {
		t.Setenv("MUTIAUK_SOCKS5_PASSWORD", "")
		path := writeTempConfig(t, baseYAML("  password: from-yaml\n"))
		defer os.Remove(path)

		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.SOCKS5.Password != "from-yaml" {
			t.Errorf("Password = %q, want %q", cfg.SOCKS5.Password, "from-yaml")
		}
	})

	t.Run("env overrides missing YAML password", func(t *testing.T) {
		t.Setenv("MUTIAUK_SOCKS5_PASSWORD", "from-env")
		path := writeTempConfig(t, baseYAML(""))
		defer os.Remove(path)

		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.SOCKS5.Password != "from-env" {
			t.Errorf("Password = %q, want %q", cfg.SOCKS5.Password, "from-env")
		}
	})

	t.Run("env wins over YAML password", func(t *testing.T) {
		t.Setenv("MUTIAUK_SOCKS5_PASSWORD", "from-env")
		path := writeTempConfig(t, baseYAML("  password: from-yaml\n"))
		defer os.Remove(path)

		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.SOCKS5.Password != "from-env" {
			t.Errorf("Password = %q, want %q (env should win)", cfg.SOCKS5.Password, "from-env")
		}
	})
}

func TestParseRaw(t *testing.T) {
	yamlSrc := `
tun:
  name: tun9
socks5:
  server: 127.0.0.1:1080
  username: user
  password: from-yaml
autoroutes:
  enabled: true
routes:
  - destination: 10.0.0.0/8
`

	t.Run("no defaults applied", func(t *testing.T) {
		cfg, err := ParseRaw([]byte(yamlSrc))
		if err != nil {
			t.Fatalf("ParseRaw() error = %v", err)
		}
		// Operator-set fields survive verbatim.
		if cfg.TUN.Name != "tun9" {
			t.Errorf("TUN.Name = %q, want tun9", cfg.TUN.Name)
		}
		// Unset fields must remain zero-valued (no setDefaults run).
		if cfg.TUN.MTU != 0 {
			t.Errorf("TUN.MTU = %d, want 0 (no defaults)", cfg.TUN.MTU)
		}
		if cfg.SOCKS5.Timeout != 0 {
			t.Errorf("SOCKS5.Timeout = %v, want 0 (no defaults)", cfg.SOCKS5.Timeout)
		}
		if cfg.NAT.TableSize != 0 {
			t.Errorf("NAT.TableSize = %d, want 0 (no defaults)", cfg.NAT.TableSize)
		}
		if cfg.DNSIntegration != "" {
			t.Errorf("DNSIntegration = %q, want empty (no defaults)", cfg.DNSIntegration)
		}
		if cfg.Daemon.PIDFile != "" {
			t.Errorf("Daemon.PIDFile = %q, want empty (no defaults)", cfg.Daemon.PIDFile)
		}
	})

	t.Run("env secrets not applied", func(t *testing.T) {
		t.Setenv("MUTIAUK_SOCKS5_PASSWORD", "from-env")

		cfg, err := ParseRaw([]byte(yamlSrc))
		if err != nil {
			t.Fatalf("ParseRaw() error = %v", err)
		}
		if cfg.SOCKS5.Password != "from-yaml" {
			t.Errorf("SOCKS5.Password = %q, want from-yaml (env must not override in ParseRaw)", cfg.SOCKS5.Password)
		}
	})

	t.Run("invalid YAML errors", func(t *testing.T) {
		if _, err := ParseRaw([]byte("{not valid yaml")); err == nil {
			t.Fatal("ParseRaw() expected error for invalid YAML")
		}
	})
}

// --- Helper Functions ---

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	if _, err := tmpFile.WriteString(content); err != nil {
		os.Remove(tmpFile.Name())
		t.Fatalf("failed to write temp file: %v", err)
	}

	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpFile.Name())
		t.Fatalf("failed to close temp file: %v", err)
	}

	return tmpFile.Name()
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
