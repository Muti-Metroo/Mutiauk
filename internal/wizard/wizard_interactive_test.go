package wizard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Muti-Metroo/Mutiauk/internal/config"
	"github.com/Muti-Metroo/Mutiauk/internal/wizard/prompt"
)

// feed scripts the prompt package's input for the interactive ask*
// functions. Each argument is one line the operator would type; an
// empty string is a bare Enter (accept the default). A trailing newline
// is appended so the final line is delivered like the others. The
// prompts' stdout is silenced for the rest of the test so scripted runs
// do not bury the test markers.
func feed(t *testing.T, lines ...string) {
	t.Helper()
	prompt.SetReader(strings.NewReader(strings.Join(lines, "\n") + "\n"))

	orig := os.Stdout
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	os.Stdout = devnull
	t.Cleanup(func() {
		os.Stdout = orig
		devnull.Close()
	})
}

func TestAskConfigPath_LoadsValidExistingConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "tun:\n  name: tun7\nsocks5:\n  server: 127.0.0.1:1080\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	w := New()
	// Config path -> Enter the typed path (dir already exists, no overwrite).
	feed(t, path)
	got, err := w.askConfigPath()
	if err != nil {
		t.Fatalf("askConfigPath error: %v", err)
	}
	if got != path {
		t.Errorf("askConfigPath() = %q, want %q", got, path)
	}
	if w.existingCfg == nil || w.existingRaw == nil {
		t.Fatal("existing config should be loaded for use as defaults")
	}
	// existingCfg carries defaults; existingRaw must not.
	if w.existingCfg.TUN.MTU == 0 {
		t.Error("existingCfg.TUN.MTU should be defaulted")
	}
	if w.existingRaw.TUN.MTU != 0 {
		t.Errorf("existingRaw.TUN.MTU = %d, want 0 (no defaults)", w.existingRaw.TUN.MTU)
	}
	if w.existingRaw.TUN.Name != "tun7" {
		t.Errorf("existingRaw.TUN.Name = %q, want tun7", w.existingRaw.TUN.Name)
	}
}

func TestAskConfigPath_CancelsOnUnparseableExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("{not valid yaml"), 0600); err != nil {
		t.Fatal(err)
	}

	w := New()
	// Config path, then decline the overwrite prompt.
	feed(t, path, "n")
	_, err := w.askConfigPath()
	if err == nil {
		t.Fatal("expected setup to cancel rather than overwrite an unparseable file")
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAskSOCKS5Config_KeepCurrentPasswordUsesRawNotEnv(t *testing.T) {
	// Prove Change 1b: the env-overridden value lives on existingCfg
	// (as Load would produce), but keep-current must source the FILE
	// value from existingRaw so the env secret is never persisted.
	t.Setenv("MUTIAUK_SOCKS5_PASSWORD", "env-pw")

	w := New()
	w.existingCfg = &config.Config{SOCKS5: config.SOCKS5Config{
		Server:   "10.1.2.3:1080",
		Username: "op",
		Password: "env-pw", // env-overridden, as Load would leave it
	}}
	w.existingRaw = &config.Config{SOCKS5: config.SOCKS5Config{
		Server:   "10.1.2.3:1080",
		Username: "op",
		Password: "file-pw", // what the operator actually wrote
	}}

	// Server (default) -> Enter; auth? (default yes, username present) -> Enter;
	// username (default) -> Enter; password keep-current -> Enter.
	feed(t, "", "", "", "")
	cfg, err := w.askSOCKS5Config()
	if err != nil {
		t.Fatalf("askSOCKS5Config error: %v", err)
	}

	if cfg.Server != "10.1.2.3:1080" {
		t.Errorf("Server = %q, want existing default", cfg.Server)
	}
	if cfg.Username != "op" {
		t.Errorf("Username = %q, want op", cfg.Username)
	}
	if cfg.Password != "file-pw" {
		t.Errorf("Password = %q, want file-pw (raw file value, NOT env-overridden)", cfg.Password)
	}
}

func TestAskAutoRoutes_EnabledByConfirm(t *testing.T) {
	w := New()
	// Existing config has autoroutes on, so the single confirm prompt
	// defaults to yes; a bare Enter keeps it enabled.
	w.existingCfg = &config.Config{AutoRoutes: config.AutoRoutesConfig{Enabled: true}}

	feed(t, "")
	cfg, useAuto, err := w.askAutoRoutes()
	if err != nil {
		t.Fatalf("askAutoRoutes error: %v", err)
	}
	if !useAuto || !cfg.Enabled {
		t.Fatalf("autoroutes should stay enabled (useAuto=%v enabled=%v)", useAuto, cfg.Enabled)
	}
}

func TestAskAutoRoutes_Declined(t *testing.T) {
	w := New()

	// Decline the single confirm prompt.
	feed(t, "n")
	cfg, useAuto, err := w.askAutoRoutes()
	if err != nil {
		t.Fatalf("askAutoRoutes error: %v", err)
	}
	if useAuto || cfg.Enabled {
		t.Fatalf("autoroutes should be disabled (useAuto=%v enabled=%v)", useAuto, cfg.Enabled)
	}
}

func TestAskRoutes_KeepsExistingAndSkipsPresetOffer(t *testing.T) {
	w := New()
	w.existingCfg = &config.Config{Routes: []config.RouteConfig{
		{Destination: "10.5.0.0/16", Comment: "lab", Enabled: true},
		{Destination: "192.168.9.0/24", Enabled: false},
	}}

	// With existing routes the preset offer is skipped; only the
	// "Add a custom route?" prompt remains -> Enter declines (default no).
	feed(t, "")
	routes, err := w.askRoutes()
	if err != nil {
		t.Fatalf("askRoutes error: %v", err)
	}

	if len(routes) != 2 {
		t.Fatalf("routes = %d, want 2 existing preserved", len(routes))
	}
	if routes[0].Destination != "10.5.0.0/16" || routes[0].Comment != "lab" || !routes[0].Enabled {
		t.Errorf("first route not preserved verbatim: %+v", routes[0])
	}
	if routes[1].Destination != "192.168.9.0/24" || routes[1].Enabled {
		t.Errorf("second route not preserved verbatim: %+v", routes[1])
	}
}

// feedCapture is feed() with stdout captured instead of discarded. The
// returned func yields everything the prompts printed so far.
func feedCapture(t *testing.T, lines ...string) func() string {
	t.Helper()
	prompt.SetReader(strings.NewReader(strings.Join(lines, "\n") + "\n"))

	orig := os.Stdout
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("create stdout capture: %v", err)
	}
	os.Stdout = f
	t.Cleanup(func() {
		os.Stdout = orig
		f.Close()
	})
	return func() string {
		b, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatalf("read stdout capture: %v", err)
		}
		return string(b)
	}
}

func TestAskSOCKS5Config_AcceptsWebSocketURL(t *testing.T) {
	w := New()

	// An invalid entry is re-prompted, then the wss:// URL is accepted;
	// decline auth.
	out := feedCapture(t, "not-an-address", "wss://relay.example.com:8443/socks5", "n")
	cfg, err := w.askSOCKS5Config()
	if err != nil {
		t.Fatalf("askSOCKS5Config error: %v", err)
	}
	if cfg.Server != "wss://relay.example.com:8443/socks5" {
		t.Errorf("Server = %q, want the wss:// URL", cfg.Server)
	}

	s := out()
	if !strings.Contains(s, "SOCKS5 server (host:port, or wss://host:port/path)") {
		t.Errorf("prompt text should mention wss:// form, got:\n%s", s)
	}
	if !strings.Contains(s, "Error:") {
		t.Errorf("invalid entry should be rejected with an error, got:\n%s", s)
	}
	if strings.Contains(s, "unencrypted") {
		t.Errorf("wss:// server must not trigger the plaintext note, got:\n%s", s)
	}
}

func TestAskSOCKS5Config_PlainRemoteNote(t *testing.T) {
	tests := []struct {
		name     string
		server   string
		wantNote bool
	}{
		{"remote host:port", "10.9.8.7:1080", true},
		{"remote hostname", "agent.example.com:1080", true},
		{"loopback IPv4", "127.0.0.1:1080", false},
		{"loopback IPv6", "[::1]:1080", false},
		{"localhost", "localhost:1080", false},
		{"ws URL", "ws://agent.example.com/socks5", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := New()
			out := feedCapture(t, tt.server, "n")
			if _, err := w.askSOCKS5Config(); err != nil {
				t.Fatalf("askSOCKS5Config error: %v", err)
			}
			s := out()
			gotNote := strings.Contains(s, "Plain SOCKS5 is unencrypted")
			if gotNote != tt.wantNote {
				t.Errorf("plaintext note shown = %v, want %v; output:\n%s", gotNote, tt.wantNote, s)
			}
			if gotNote && strings.Count(s, "Plain SOCKS5 is unencrypted") != 1 {
				t.Errorf("plaintext note should be printed once; output:\n%s", s)
			}
		})
	}
}

func TestAskSOCKS5Config_PreservesRawTransportSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "" +
		"tun:\n  name: tun0\n  address: 10.200.200.1/24\n" +
		"socks5:\n  server: relay.example.com:8443\n" +
		"  transport: websocket\n  ws_path: /custom\n" +
		"  timeout: 45s\n  keepalive: 20s\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	w := New()
	// Config path, then server (keep) and auth (default no).
	feed(t, path, "", "")
	if _, err := w.askConfigPath(); err != nil {
		t.Fatalf("askConfigPath error: %v", err)
	}
	cfg, err := w.askSOCKS5Config()
	if err != nil {
		t.Fatalf("askSOCKS5Config error: %v", err)
	}

	if cfg.Server != "relay.example.com:8443" {
		t.Errorf("Server = %q, want existing value", cfg.Server)
	}
	if cfg.Transport != "websocket" || cfg.WSPath != "/custom" {
		t.Errorf("transport/ws_path = %q/%q, want websocket//custom", cfg.Transport, cfg.WSPath)
	}
	if cfg.Timeout.String() != "45s" || cfg.KeepAlive.String() != "20s" {
		t.Errorf("timeout/keepalive = %v/%v, want 45s/20s", cfg.Timeout, cfg.KeepAlive)
	}
	// websocket transport wraps host:port in wss://, so no plaintext note.
	if isPlainRemoteSOCKS5(cfg) {
		t.Error("websocket transport must not be reported as plain SOCKS5")
	}
}

func TestAskSOCKS5Config_EditDoesNotFreezeTransportDefaults(t *testing.T) {
	// A config that never set transport/ws_path must not gain the
	// defaulted values ("tcp", "/socks5") from existingCfg on re-save.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "tun:\n  name: tun0\nsocks5:\n  server: 127.0.0.1:1080\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	w := New()
	feed(t, path, "", "")
	if _, err := w.askConfigPath(); err != nil {
		t.Fatalf("askConfigPath error: %v", err)
	}
	cfg, err := w.askSOCKS5Config()
	if err != nil {
		t.Fatalf("askSOCKS5Config error: %v", err)
	}
	if cfg.Transport != "" || cfg.WSPath != "" {
		t.Errorf("transport/ws_path = %q/%q, want both empty (no frozen defaults)", cfg.Transport, cfg.WSPath)
	}
}

func TestPrintSummary_ShowsTransport(t *testing.T) {
	tests := []struct {
		socks5 config.SOCKS5Config
		want   string
	}{
		{config.SOCKS5Config{Server: "wss://relay.example.com:8443/socks5"}, "WebSocket over TLS (wss://)"},
		{config.SOCKS5Config{Server: "ws://relay.example.com/socks5"}, "WebSocket without TLS"},
		{config.SOCKS5Config{Server: "relay.example.com:8443", Transport: "websocket", WSPath: "/x"}, "wss://, path /x"},
		{config.SOCKS5Config{Server: "10.0.0.1:1080"}, "plain SOCKS5 over TCP"},
	}
	for _, tt := range tests {
		t.Run(tt.socks5.Server, func(t *testing.T) {
			w := New()
			out := feedCapture(t)
			cfg := &config.Config{
				TUN:    config.TUNConfig{Name: "tun0", MTU: 1400, Address: "10.200.200.1/24"},
				SOCKS5: tt.socks5,
			}
			w.printSummary(cfg, "/tmp/config.yaml")
			s := out()
			if !strings.Contains(s, "Transport:") || !strings.Contains(s, tt.want) {
				t.Errorf("summary should show transport %q, got:\n%s", tt.want, s)
			}
		})
	}
}
