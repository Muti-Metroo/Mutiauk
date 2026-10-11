package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/postalsys/mutiauk/internal/config"
	"github.com/postalsys/mutiauk/internal/nat"
	"github.com/postalsys/mutiauk/internal/proxy"
	"github.com/postalsys/mutiauk/internal/route"
	"go.uber.org/zap"
)

// testConfig returns a minimal but valid daemon config for reload tests.
func testConfig() *config.Config {
	cfg := &config.Config{}
	cfg.TUN.Name = "mutiauk-test0"
	cfg.TUN.MTU = 1400
	cfg.SOCKS5.Server = "127.0.0.1:1080"
	cfg.SOCKS5.Timeout = 30 * time.Second
	cfg.SOCKS5.KeepAlive = 60 * time.Second
	cfg.SOCKS5.Transport = "tcp"
	cfg.Daemon.StatusProbeTarget = "example.com:80"
	return cfg
}

// stubNetwork wires the forwarders Reload touches with throwaway
// implementations so a reload can run without a TUN device or gVisor
// stack. The route manager points at a nonexistent interface, so route
// diffs come back empty and Apply is a no-op.
func stubNetwork(t *testing.T, s *Server) {
	t.Helper()
	logger := zap.NewNop()
	natTable := nat.NewTable(nat.Config{})
	s.network.natTable = natTable
	s.network.socks5Client = s.createSOCKS5Client(s.cfg.Load())
	s.network.tcpForwarder = proxy.NewTCPForwarder(s.network.socks5Client, natTable, logger)
	s.network.udpForwarder = proxy.NewUDPForwarder(s.network.socks5Client, natTable, logger)
	s.network.rawUDPForwarder = proxy.NewRawUDPForwarder(s.network.socks5Client, logger)
	s.network.icmpForwarder = proxy.NewICMPForwarder(s.network.socks5Client, logger, time.Second)
	mgr, err := route.NewManager(s.cfg.Load().TUN.Name, logger)
	if err != nil {
		t.Fatalf("route.NewManager: %v", err)
	}
	s.routeMgr = mgr
}

func TestReloadFromDiskRejectsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// tun.mtu below the 576 minimum parses fine but fails Validate.
	content := []byte("tun:\n  name: mutiauk0\n  mtu: 100\nsocks5:\n  server: 127.0.0.1:1080\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := New(testConfig(), path, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	if err := s.ReloadFromDisk(); err == nil {
		t.Fatal("ReloadFromDisk accepted an invalid config")
	}
	// The running config must be untouched.
	if got := s.cfg.Load().TUN.MTU; got != 1400 {
		t.Fatalf("stored config mutated by rejected reload: mtu=%d", got)
	}
}

func TestReloadFromDiskAppliesValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte("tun:\n  name: mutiauk-test0\n  mtu: 1400\nsocks5:\n  server: 127.0.0.1:1081\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := New(testConfig(), path, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	stubNetwork(t, s)

	if err := s.ReloadFromDisk(); err != nil {
		t.Fatalf("ReloadFromDisk: %v", err)
	}
	if got := s.cfg.Load().SOCKS5.Server; got != "127.0.0.1:1081" {
		t.Fatalf("socks5.server = %q, want 127.0.0.1:1081", got)
	}
	if got := s.network.socks5Client.ServerAddr; got != "127.0.0.1:1081" {
		t.Fatalf("forwarder client server = %q, want 127.0.0.1:1081", got)
	}
}

func TestReloadRestartsWatcherOnServerChange(t *testing.T) {
	cfg := testConfig()
	cfg.AutoRoutes.Enabled = true
	s, err := New(cfg, "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	stubNetwork(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.servicesMu.Lock()
	s.servicesCtx = ctx
	s.servicesMu.Unlock()

	s.startAutoRoutes(ctx)
	s.servicesMu.Lock()
	first := s.watcher
	s.servicesMu.Unlock()
	if first == nil {
		t.Fatal("no watcher registered after startAutoRoutes")
	}

	next := *cfg
	next.SOCKS5.Server = "127.0.0.1:1081"
	if err := s.Reload(&next); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	s.servicesMu.Lock()
	second := s.watcher
	s.servicesMu.Unlock()
	if second == nil || second == first {
		t.Fatal("watcher was not restarted on socks5.server change")
	}
	select {
	case <-first.ctx.Done():
	default:
		t.Error("previous watcher was not canceled")
	}
	select {
	case <-second.ctx.Done():
		t.Error("new watcher context is already canceled")
	default:
	}
}

func TestReloadKeepsWatcherWhenServerUnchanged(t *testing.T) {
	cfg := testConfig()
	cfg.AutoRoutes.Enabled = true
	s, err := New(cfg, "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	stubNetwork(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.servicesMu.Lock()
	s.servicesCtx = ctx
	s.servicesMu.Unlock()

	s.startAutoRoutes(ctx)
	s.servicesMu.Lock()
	first := s.watcher
	s.servicesMu.Unlock()

	next := *cfg // identical SOCKS5 config
	if err := s.Reload(&next); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	s.servicesMu.Lock()
	second := s.watcher
	s.servicesMu.Unlock()
	if second != first {
		t.Error("watcher restarted although the SOCKS5 config did not change")
	}
	select {
	case <-first.ctx.Done():
		t.Error("watcher canceled although the SOCKS5 config did not change")
	default:
	}
}

func TestSocks5ConfigChangedIncludesTimeoutAndKeepAlive(t *testing.T) {
	s, err := New(testConfig(), "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	base := testConfig()

	same := *base
	if s.socks5ConfigChanged(base, &same) {
		t.Error("identical config reported as changed")
	}

	timeoutChange := *base
	timeoutChange.SOCKS5.Timeout = 5 * time.Second
	if !s.socks5ConfigChanged(base, &timeoutChange) {
		t.Error("socks5.timeout change not detected")
	}

	keepAliveChange := *base
	keepAliveChange.SOCKS5.KeepAlive = 10 * time.Second
	if !s.socks5ConfigChanged(base, &keepAliveChange) {
		t.Error("socks5.keepalive change not detected")
	}
}

func TestRestartRequiredChanges(t *testing.T) {
	base := testConfig()

	if got := restartRequiredChanges(base, testConfig()); len(got) != 0 {
		t.Errorf("identical configs: got %v, want none", got)
	}

	tunChange := *base
	tunChange.TUN.MTU = 1500
	assertSections(t, restartRequiredChanges(base, &tunChange), "tun")

	arChange := *base
	arChange.AutoRoutes.Enabled = true
	assertSections(t, restartRequiredChanges(base, &arChange), "autoroutes")

	dnsChange := *base
	dnsChange.DNSIntegration = "none"
	assertSections(t, restartRequiredChanges(base, &dnsChange), "dns_integration")

	logChange := *base
	logChange.Logging.Level = "debug"
	assertSections(t, restartRequiredChanges(base, &logChange), "logging")

	adminChange := *base
	adminChange.Admin.Listen = "127.0.0.1:9999"
	assertSections(t, restartRequiredChanges(base, &adminChange), "admin")

	// Applied-live sections must not be flagged.
	socksChange := *base
	socksChange.SOCKS5.Server = "127.0.0.1:1082"
	if got := restartRequiredChanges(base, &socksChange); len(got) != 0 {
		t.Errorf("socks5-only change flagged for restart: %v", got)
	}
}

func assertSections(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("sections = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sections = %v, want %v", got, want)
		}
	}
}
