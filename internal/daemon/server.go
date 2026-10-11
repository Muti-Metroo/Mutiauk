package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Muti-Metroo/Mutiauk/internal/adminhttp"
	"github.com/Muti-Metroo/Mutiauk/internal/api"
	"github.com/Muti-Metroo/Mutiauk/internal/autoroutes"
	"github.com/Muti-Metroo/Mutiauk/internal/config"
	meshdns "github.com/Muti-Metroo/Mutiauk/internal/dns"
	"github.com/Muti-Metroo/Mutiauk/internal/expose"
	"github.com/Muti-Metroo/Mutiauk/internal/nat"
	"github.com/Muti-Metroo/Mutiauk/internal/proxy"
	"github.com/Muti-Metroo/Mutiauk/internal/route"
	"github.com/Muti-Metroo/Mutiauk/internal/socks5"
	"github.com/Muti-Metroo/Mutiauk/internal/stack"
	"github.com/Muti-Metroo/Mutiauk/internal/tun"
	"go.uber.org/zap"
)

// routeState manages both config and auto routes with unified locking
type routeState struct {
	mu         sync.RWMutex
	autoRoutes []route.Route
}

func (rs *routeState) setAutoRoutes(routes []route.Route) {
	rs.mu.Lock()
	rs.autoRoutes = routes
	rs.mu.Unlock()
}

func (rs *routeState) getAutoRoutes() []route.Route {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	if rs.autoRoutes == nil {
		return nil
	}
	result := make([]route.Route, len(rs.autoRoutes))
	copy(result, rs.autoRoutes)
	return result
}

func (rs *routeState) autoRouteCount() int {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return len(rs.autoRoutes)
}

// networkStack holds all network-related components
type networkStack struct {
	tunDev          tun.Device
	netStack        *stack.Stack
	natTable        *nat.Table
	socks5Client    *socks5.Client
	tcpForwarder    *proxy.TCPForwarder
	rawUDPForwarder *proxy.RawUDPForwarder
	icmpForwarder   *proxy.ICMPForwarder
}

// Server is the main daemon server
type Server struct {
	cfg        atomic.Pointer[config.Config]
	configPath string
	logger     *zap.Logger

	mu        sync.RWMutex
	running   bool
	startTime time.Time
	cancelFn  context.CancelFunc

	network     networkStack
	routeMgr    *route.Manager
	routeState  routeState
	routeEvents routeEventLog
	apiServer   *api.Server
	adminServer *adminhttp.Server
	exposeMgr   *expose.Manager
	// dnsMu guards dnsServer, dnsListenIP and dnsIntegration. The DNS
	// server can (re)start after startup - the route-watch callback retries
	// a failed start - so these fields are written past initialize().
	dnsMu          sync.RWMutex
	dnsServer      *meshdns.Server
	dnsListenIP    string
	dnsIntegration meshdns.Integration

	// servicesMu guards servicesCtx and watcher.
	servicesMu  sync.Mutex
	servicesCtx context.Context // daemon-scoped context from startServices
	watcher     *watcherHandle  // running autoroutes watcher, if any

	// protectedIPs are the resolved SOCKS5 server addresses that must never
	// be routed into the TUN: doing so would route the daemon's own upstream
	// connection through the tunnel and loop, breaking all networking.
	// Resolved at startup and on reload.
	protectedMu  sync.RWMutex
	protectedIPs []net.IP

	// refused holds the routes most recently declined by syncRoutes because
	// they cover a protected SOCKS5 server address. Surfaced via the API and
	// admin dashboard (service deployments never see the WARN logs).
	// refusedLogged dedups the WARN so a persistent poison route is logged
	// once, not on every route-watch push.
	refusedMu     sync.Mutex
	refused       []api.RefusedRoute
	refusedLogged map[string]bool

	// applyErrLogged dedupes the "failed to apply routes" Warn so one poison
	// route does not spam the log on every route-watch push; a successful
	// apply re-arms it.
	applyErrMu     sync.Mutex
	applyErrLogged bool
	lastApplyErr   string

	appliedMu sync.Mutex
	applied   appliedDNSState // last-applied state; closes the diff cache
}

// watcherHandle tracks a running autoroutes watcher so it can be stopped
// when the SOCKS5 config changes (the watcher pins its client at
// construction) or when the daemon shuts down.
type watcherHandle struct {
	ctx     context.Context
	cancel  context.CancelFunc
	watcher *autoroutes.Watcher
}

// appliedDNSState is the diff key used by applyDNSIntegration to skip
// no-op updates. It includes the listen IP so a TUN re-address mid-run
// triggers a re-apply even when the suffix list itself didn't change.
type appliedDNSState struct {
	listenIP string
	suffixes []string // canonical (sorted, lowercased)
	at       time.Time
}

// New creates a new daemon server
func New(cfg *config.Config, configPath string, logger *zap.Logger) (*Server, error) {
	s := &Server{
		configPath: configPath,
		logger:     logger,
	}
	s.cfg.Store(cfg)
	return s, nil
}

// Run starts the daemon server
func (s *Server) Run(ctx context.Context) error {
	if err := s.setRunning(true); err != nil {
		return err
	}
	defer s.setRunning(false)

	ctx, cancel := context.WithCancel(ctx)
	s.cancelFn = cancel
	defer cancel()

	// Single-instance guard: lock the PID file for the daemon's lifetime.
	// A second daemon refuses to start instead of overwriting the PID file,
	// stealing the API socket, and fighting over host DNS state. The lock
	// write doubles as writePIDFile.
	lock, err := acquireInstanceLock(s.cfg.Load().Daemon.PIDFile)
	if err != nil {
		return err
	}
	// Release runs before removePIDFile (LIFO) so the file is closed -
	// required on Windows, where an open locked file cannot be deleted.
	defer s.removePIDFile()
	defer lock.Release()

	if err := s.initialize(); err != nil {
		// Roll back whatever initialize managed to create (TUN device,
		// stack, forwarders) so a partial init does not leak resources.
		// cleanup nil-checks every component.
		s.cleanup()
		return fmt.Errorf("initialization failed: %w", err)
	}
	defer s.cleanup()

	s.startServices(ctx)

	cfg := s.cfg.Load()
	s.logger.Info("daemon started",
		zap.String("tun", s.ifaceName()),
		zap.String("socks5", cfg.SOCKS5.Server),
		zap.Bool("autoroutes", cfg.AutoRoutes.Enabled),
	)

	return s.network.netStack.Run(ctx)
}

// setRunning atomically sets the running state
func (s *Server) setRunning(running bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if running && s.running {
		return fmt.Errorf("server already running")
	}
	s.running = running
	if running {
		s.startTime = time.Now()
	}
	return nil
}

// startServices starts API server, routes, autoroutes poller, and NAT GC
func (s *Server) startServices(ctx context.Context) {
	cfg := s.cfg.Load()

	// Store the daemon-scoped context so reload-triggered restarts (the
	// autoroutes watcher) and retries (the DNS server) can spawn
	// replacements with the same lifetime.
	s.servicesMu.Lock()
	s.servicesCtx = ctx
	s.servicesMu.Unlock()

	if cfg.Daemon.SocketPath != "" {
		s.apiServer = api.NewServer(cfg.Daemon.SocketPath, s, s.logger)
		if err := s.apiServer.Start(ctx); err != nil {
			s.logger.Warn("failed to start API server", zap.Error(err))
		}
	}

	if cfg.Admin.AdminEnabled() {
		token, err := adminhttp.LoadOrCreateToken(s.configPath)
		if err != nil {
			// Fail closed: without a token the admin interface would be
			// unusable anyway, and serving it unauthenticated is not an
			// option.
			s.logger.Warn("admin HTTP interface disabled: failed to load admin token", zap.Error(err))
		} else {
			s.adminServer = adminhttp.NewServer(s, cfg.Admin, token, s.logger)
			if err := s.adminServer.Start(ctx); err != nil {
				s.logger.Warn("failed to start admin HTTP interface", zap.Error(err))
				s.adminServer = nil
			} else {
				s.logger.Info("admin HTTP interface requires a token; run `mutiauk admin` to print a dashboard login URL",
					zap.String("token_file", adminhttp.TokenPath(s.configPath)))
			}
		}
	}

	// Resolve the protected SOCKS5 server address before applying any routes
	// so syncRoutes can refuse a route that would cover it.
	s.refreshProtectedServerIPs()

	if err := s.syncRoutes(); err != nil {
		s.warnApplyFailure("failed to apply some routes", err)
	}

	// Clear any stale host-DNS state (macOS /etc/resolver files, NM
	// dnsmasq drop-ins, Windows NRPT rules) left by a prior unclean
	// shutdown. This runs regardless of whether autoroutes is enabled
	// now: the previous run may have had it enabled, and stale resolver
	// files pointing at a dead TUN IP cause DNS timeouts for those TLDs.
	// RevertAll is idempotent and best-effort; failures are only logged.
	meshdns.RevertAll(s.ifaceName(), s.logger)

	if cfg.AutoRoutes.Enabled {
		if err := s.startDNS(ctx); err != nil {
			s.logger.Warn("failed to start DNS server", zap.Error(err))
		}
		s.startAutoRoutes(ctx)
	}

	if cfg.Expose.Enabled {
		mgr, err := expose.NewManager(cfg.Expose, s.network.socks5Client, s.logger)
		if err != nil {
			s.logger.Warn("failed to create expose manager", zap.Error(err))
		} else {
			s.exposeMgr = mgr
			mgr.Start(ctx)
		}
	}

	s.network.natTable.StartGC(ctx, nat.GCConfig{
		Interval: cfg.NAT.GCInterval,
		Logger:   s.logger,
	})
}

// initialize sets up all components
func (s *Server) initialize() error {
	cfg := s.cfg.Load()

	tunCfg, err := s.parseTUNConfig(cfg)
	if err != nil {
		return err
	}

	if err := s.initNetwork(cfg, tunCfg); err != nil {
		return err
	}

	s.routeMgr, err = route.NewManager(s.ifaceName(), s.logger)
	if err != nil {
		return fmt.Errorf("failed to create route manager: %w", err)
	}

	return nil
}

// ifaceName returns the live network interface name. On macOS the kernel
// assigns the utun name (e.g. "utun4"), which can differ from the configured
// name, so every OS-facing operation (routes, DNS integration, lookups) must
// use this rather than cfg.TUN.Name. Falls back to the configured name before
// the device exists.
func (s *Server) ifaceName() string {
	if s.network.tunDev != nil {
		return s.network.tunDev.Name()
	}
	return s.cfg.Load().TUN.Name
}

// parseTUNConfig parses TUN configuration from config
func (s *Server) parseTUNConfig(cfg *config.Config) (tun.Config, error) {
	tunCfg := tun.Config{
		Name:    cfg.TUN.Name,
		MTU:     cfg.TUN.MTU,
		Persist: false,
	}

	if cfg.TUN.Address != "" {
		ip, ipNet, err := net.ParseCIDR(cfg.TUN.Address)
		if err != nil {
			return tunCfg, fmt.Errorf("invalid TUN address: %w", err)
		}
		tunCfg.Address = ip
		tunCfg.Netmask = ipNet.Mask
	}

	if cfg.TUN.Address6 != "" {
		ip, ipNet, err := net.ParseCIDR(cfg.TUN.Address6)
		if err != nil {
			return tunCfg, fmt.Errorf("invalid TUN IPv6 address: %w", err)
		}
		tunCfg.Address6 = ip
		tunCfg.Netmask6 = ipNet.Mask
	}

	return tunCfg, nil
}

// initNetwork initializes all network components
func (s *Server) initNetwork(cfg *config.Config, tunCfg tun.Config) error {
	var err error

	s.network.tunDev, err = tun.New(tunCfg)
	if err != nil {
		return fmt.Errorf("failed to create TUN device: %w", err)
	}
	s.logger.Info("TUN device created", zap.String("name", s.network.tunDev.Name()))

	s.network.natTable = nat.NewTable(nat.Config{
		MaxEntries: cfg.NAT.TableSize,
		TCPTTL:     cfg.NAT.TCPTimeout,
		UDPTTL:     cfg.NAT.UDPTimeout,
	})

	s.network.socks5Client = s.createSOCKS5Client(cfg)
	s.network.tcpForwarder = proxy.NewTCPForwarder(s.network.socks5Client, s.network.natTable, s.logger)
	s.network.rawUDPForwarder = proxy.NewRawUDPForwarder(s.network.socks5Client, s.logger)
	s.network.icmpForwarder = proxy.NewICMPForwarder(s.network.socks5Client, s.logger, cfg.SOCKS5.Timeout)

	stackCfg := stack.Config{
		MTU:              cfg.TUN.MTU,
		TCPHandler:       s.network.tcpForwarder,
		RawUDPHandler:    s.network.rawUDPForwarder,
		ICMPHandler:      s.network.icmpForwarder,
		ICMPErrorReplies: cfg.ICMP.EmitUnreachable(),
		Logger:           s.logger,
	}

	if tunCfg.Address != nil {
		stackCfg.IPv4Addr = tunCfg.Address
		stackCfg.IPv4Mask = tunCfg.Netmask
	}
	if tunCfg.Address6 != nil {
		stackCfg.IPv6Addr = tunCfg.Address6
		stackCfg.IPv6Mask = tunCfg.Netmask6
	}

	s.network.netStack, err = stack.New(s.network.tunDev, stackCfg)
	if err != nil {
		return fmt.Errorf("failed to create network stack: %w", err)
	}

	return nil
}

// createSOCKS5Client creates a SOCKS5 client from the given config
func (s *Server) createSOCKS5Client(cfg *config.Config) *socks5.Client {
	auth := socks5.NewAuthenticator(cfg.SOCKS5.Username, cfg.SOCKS5.Password)
	return socks5.NewClientWithOptions(
		cfg.SOCKS5.Server,
		auth,
		cfg.SOCKS5.Timeout,
		cfg.SOCKS5.KeepAlive,
		socks5.ClientOptions{
			Transport:  cfg.SOCKS5.Transport,
			WSPath:     cfg.SOCKS5.WSPath,
			WSUsername: cfg.SOCKS5.Username, // Use same credentials for WebSocket Basic Auth
			WSPassword: cfg.SOCKS5.Password,
		},
	)
}

// cleanup closes all components
func (s *Server) cleanup() {
	// Stop the autoroutes watcher explicitly: the deferred cancel in Run
	// runs after cleanup, so without this the watcher would keep applying
	// routes and DNS state while the rest of the daemon is being torn down.
	s.servicesMu.Lock()
	if s.watcher != nil {
		s.watcher.cancel()
		s.watcher = nil
	}
	s.servicesMu.Unlock()

	// Serialize the teardown Revert against a concurrent autoroutes Apply
	// via appliedMu, held for the whole check+revert.
	_, _, dnsIntegration := s.dnsState()
	if dnsIntegration != nil {
		s.appliedMu.Lock()
		hadApplied := s.applied.listenIP != "" || len(s.applied.suffixes) > 0
		if hadApplied {
			if err := dnsIntegration.Revert(); err != nil {
				s.logger.Warn("dns integration revert failed",
					zap.Error(err),
					zap.String("integration", dnsIntegration.Name()),
				)
			}
		}
		s.appliedMu.Unlock()
	}
	if s.exposeMgr != nil {
		s.exposeMgr.Stop()
	}
	if s.adminServer != nil {
		s.adminServer.Stop()
	}
	if s.apiServer != nil {
		s.apiServer.Stop()
	}
	if s.network.icmpForwarder != nil {
		s.network.icmpForwarder.Close()
	}
	if s.network.rawUDPForwarder != nil {
		s.network.rawUDPForwarder.Close()
	}
	if s.network.netStack != nil {
		s.network.netStack.Close()
	}
	if s.network.tunDev != nil {
		s.network.tunDev.Close()
	}
	if s.network.natTable != nil {
		s.network.natTable.Clear()
	}
}

// startAutoRoutes watches the agent's route-watch stream (push) over the
// SOCKS5 endpoint and applies route/host/DNS state on each snapshot. When the
// agent does not support route-watch, autoroutes simply does not start.
//
// The watcher pins its SOCKS5 client at construction, so it must be
// restarted when the SOCKS5 config changes (Reload does this). Starting a
// new watcher cancels the previous one.
func (s *Server) startAutoRoutes(ctx context.Context) {
	cfg := s.cfg.Load()

	watcher := autoroutes.NewWatcher(
		s.createSOCKS5Client(cfg),
		s.ifaceName(),
		s.onDashboardUpdate,
		s.logger,
	)

	wctx, wcancel := context.WithCancel(ctx)
	handle := &watcherHandle{ctx: wctx, cancel: wcancel, watcher: watcher}
	s.servicesMu.Lock()
	if s.watcher != nil {
		s.watcher.cancel()
	}
	s.watcher = handle
	s.servicesMu.Unlock()

	s.logger.Info("starting autoroutes route-watch over SOCKS5",
		zap.String("server", cfg.SOCKS5.Server),
	)

	// By default auto routes ride out an agent outage indefinitely; with
	// autoroutes.stale_timeout configured, flush them once no snapshot has
	// arrived for that long.
	if timeout := cfg.AutoRoutes.StaleTimeout; timeout > 0 {
		go s.watchStaleAutoRoutes(wctx, watcher, timeout)
	}

	go func() {
		for {
			if wctx.Err() != nil {
				return
			}
			// watcher.Run returns only on context cancellation or
			// ErrUnsupported; any other return means a panic escaped the
			// watcher. Recover, log, and restart it so one bad snapshot
			// does not kill autoroutes for the process lifetime.
			err := func() (err error) {
				defer func() {
					if r := recover(); r != nil {
						s.logger.Error("panic in autoroutes watcher; restarting",
							zap.Any("panic", r),
							zap.Stack("stacktrace"),
						)
						err = fmt.Errorf("panic: %v", r)
					}
				}()
				return watcher.Run(wctx)
			}()
			if errors.Is(err, autoroutes.ErrUnsupported) {
				s.logger.Error("agent does not support route-watch; autoroutes disabled")
				return
			}
			if err == nil || wctx.Err() != nil {
				return
			}
			select {
			case <-wctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
}

// startDNS binds the local mesh DNS resolver to the TUN IPv4 address
// on port 53. The resolver starts with no active suffixes; the
// autoroutes poller delivers the suffix set on each tick and
// applyDNSIntegration drives whichever host backend was selected.
func (s *Server) startDNS(ctx context.Context) error {
	cfg := s.cfg.Load()

	addr, err := resolveDNSListen(cfg)
	if err != nil {
		return err
	}
	listenIP, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("dns listen address %q: %w", addr, err)
	}

	integration, err := meshdns.DetectIntegration(cfg.DNSIntegration, s.ifaceName(), listenIP, s.logger)
	if err != nil {
		return fmt.Errorf("detect dns integration: %w", err)
	}

	srv, err := meshdns.New(meshdns.Config{Logger: s.logger})
	if err != nil {
		return fmt.Errorf("create dns server: %w", err)
	}

	if err := srv.Listen(ctx, addr); err != nil {
		return fmt.Errorf("dns listen on %s: %w", addr, err)
	}

	s.dnsMu.Lock()
	s.dnsServer = srv
	s.dnsListenIP = listenIP
	s.dnsIntegration = integration
	s.dnsMu.Unlock()
	s.logger.Info("mesh DNS server started",
		zap.String("addr", addr),
		zap.String("integration", integration.Name()),
	)
	return nil
}

// dnsState returns the current mesh DNS server, its listen IP, and the
// selected host integration under dnsMu.
func (s *Server) dnsState() (*meshdns.Server, string, meshdns.Integration) {
	s.dnsMu.RLock()
	defer s.dnsMu.RUnlock()
	return s.dnsServer, s.dnsListenIP, s.dnsIntegration
}

// retryStartDNS restarts the mesh DNS server when it is not running, e.g.
// because the initial bind failed at startup. Called from the route-watch
// callback so a transient failure heals on the next snapshot instead of
// leaving mesh DNS dead for the process lifetime. A concurrent duplicate
// start is harmless: the second bind on the same address fails and is
// logged.
func (s *Server) retryStartDNS() {
	s.dnsMu.RLock()
	running := s.dnsServer != nil
	s.dnsMu.RUnlock()
	if running {
		return
	}

	s.servicesMu.Lock()
	ctx := s.servicesCtx
	s.servicesMu.Unlock()
	if ctx == nil || ctx.Err() != nil {
		return
	}

	s.logger.Info("retrying mesh DNS server start")
	if err := s.startDNS(ctx); err != nil {
		s.logger.Warn("mesh DNS server retry failed", zap.Error(err))
	}
}

// resolveDNSListen returns the bind address for the DNS resolver:
// the TUN IPv4 address on port 53.
func resolveDNSListen(cfg *config.Config) (string, error) {
	ip, _, err := cfg.GetTUNIPv4()
	if err != nil {
		return "", fmt.Errorf("dns listen requires tun.address: %w", err)
	}
	if ip == nil {
		return "", fmt.Errorf("dns listen requires tun.address")
	}
	return net.JoinHostPort(ip.String(), "53"), nil
}

// onDashboardUpdate is invoked once per applied route-watch snapshot. It
// applies routes, mesh suffixes, and mesh hosts atomically: in-memory state
// is updated before resolvectl is reconfigured so systemd-resolved
// never forwards a query for a newly-added suffix into a resolver
// that doesn't yet know any hosts under it.
func (s *Server) onDashboardUpdate(routes []route.Route, suffixes []string, hosts []autoroutes.DashboardMeshHost) {
	s.routeState.setAutoRoutes(routes)
	s.warnApplyFailure("failed to apply autoroutes", s.syncRoutes())

	dnsServer, _, _ := s.dnsState()
	if dnsServer == nil {
		// The DNS server may have failed to bind at startup; retry so mesh
		// DNS is not dead for the whole process lifetime.
		s.retryStartDNS()
		dnsServer, _, _ = s.dnsState()
	}
	if dnsServer == nil {
		return
	}

	canonicalSuffixes := dnsServer.SetState(suffixes, dashboardHostsToMeshHosts(hosts, s.logger))
	s.applyDNSIntegration(canonicalSuffixes)
}

// dashboardHostsToMeshHosts validates IPs and converts dashboard
// hosts into the in-memory resolver representation, dropping entries
// with invalid or missing addresses.
func dashboardHostsToMeshHosts(hosts []autoroutes.DashboardMeshHost, logger *zap.Logger) []meshdns.Host {
	out := make([]meshdns.Host, 0, len(hosts))
	for _, h := range hosts {
		entry := meshdns.Host{Name: h.Name, Comment: h.Comment}
		if h.IPv4 != "" {
			ip := net.ParseIP(h.IPv4)
			if ip == nil || ip.To4() == nil {
				logger.Warn("dropping mesh host with invalid ipv4",
					zap.String("name", h.Name),
					zap.String("ipv4", h.IPv4),
				)
				continue
			}
			entry.IPv4 = ip.To4()
		}
		if h.IPv6 != "" {
			ip := net.ParseIP(h.IPv6)
			if ip == nil || ip.To4() != nil {
				logger.Warn("dropping mesh host with invalid ipv6",
					zap.String("name", h.Name),
					zap.String("ipv6", h.IPv6),
				)
				continue
			}
			entry.IPv6 = ip
		}
		if entry.IPv4 == nil && entry.IPv6 == nil {
			logger.Warn("dropping mesh host with no IPs", zap.String("name", h.Name))
			continue
		}
		out = append(out, entry)
	}
	return out
}

// applyDNSIntegration drives the chosen host integration to match
// `next`. No-ops when neither the suffix list nor the listen IP
// changed since the last successful apply. On shrink to empty the
// integration's Revert is invoked. `next` must be in canonical
// (sorted) order; the caller passes the slice returned by
// dns.Server.SetState.
func (s *Server) applyDNSIntegration(next []string) {
	_, listenIP, integration := s.dnsState()
	if integration == nil {
		return
	}

	s.appliedMu.Lock()
	defer s.appliedMu.Unlock()

	if s.applied.listenIP == listenIP && slices.Equal(s.applied.suffixes, next) {
		return
	}

	if len(next) == 0 {
		if err := integration.Revert(); err != nil {
			s.logger.Warn("dns integration revert failed",
				zap.Error(err),
				zap.String("integration", integration.Name()),
			)
			return
		}
		s.applied = appliedDNSState{at: time.Now()}
		return
	}

	if err := integration.Apply(next); err != nil {
		s.logger.Warn("dns integration apply failed",
			zap.Error(err),
			zap.String("integration", integration.Name()),
			zap.Strings("suffixes", next),
		)
		return
	}
	s.applied = appliedDNSState{
		listenIP: listenIP,
		suffixes: slices.Clone(next),
		at:       time.Now(),
	}
}

// getConfigRoutes returns routes from configuration
func (s *Server) getConfigRoutes() []route.Route {
	cfg := s.cfg.Load()
	var routes []route.Route

	for _, r := range cfg.Routes {
		if !r.Enabled {
			continue
		}

		_, ipNet, err := net.ParseCIDR(r.Destination)
		if err != nil {
			s.logger.Warn("invalid route destination",
				zap.String("destination", r.Destination),
				zap.Error(err),
			)
			continue
		}

		routes = append(routes, route.Route{
			Destination: ipNet,
			Interface:   s.ifaceName(),
			Comment:     r.Comment,
			Enabled:     true,
		})
	}

	return routes
}

// syncRoutes synchronizes all routes (config + auto) with the system
func (s *Server) syncRoutes() error {
	configRoutes := s.getConfigRoutes()
	autoRoutes := s.routeState.getAutoRoutes()

	desired := configRoutes
	if len(autoRoutes) > 0 {
		desired = autoroutes.MergeRoutes(configRoutes, autoRoutes)
		s.logger.Debug("applying routes",
			zap.Int("config_routes", len(configRoutes)),
			zap.Int("auto_routes", len(autoRoutes)),
			zap.Int("combined", len(desired)),
		)
	}

	// Never route the upstream SOCKS5 server into the TUN: the daemon's own
	// connection to it would then loop through the tunnel and break all
	// networking. Drop any desired route whose CIDR covers a protected server
	// IP and surface it (API/dashboard + a one-time WARN) so a poisoned
	// autoroute is visible rather than silently fatal.
	desired, refused := splitProtectedRoutes(desired, s.protectedServerIPs(), destSet(autoRoutes), destSet(configRoutes))
	s.setRefusedRoutes(refused)

	// Diff against the kernel; in steady state the plan is empty and Apply
	// records no events.
	plan, err := s.routeMgr.Diff(desired)
	if err != nil {
		return err
	}

	// The kernel auto-creates a host route for the interface's own
	// address (e.g. 10.200.200.1/32) and re-adds it as soon as it is
	// deleted. Diff sees it as "not desired" and flags it for removal on
	// every reconcile, so the daemon would otherwise fight the kernel
	// (a remove + OS re-add) on every route-watch push. Drop the
	// interface's own address routes from the removal set so they are
	// neither touched nor logged.
	self := s.selfRouteSet()
	plan.ToRemove = filterOutDests(plan.ToRemove, self)

	// Record events from the apply result, not the plan: a failed apply must
	// show up as a failure, not as a successful add/remove.
	res, err := s.routeMgr.Apply(plan)
	s.routeEvents.recordResult(res, destSet(autoRoutes), destSet(configRoutes), self)
	return err
}

// warnApplyFailure logs a route-apply failure at Warn, suppressing repeats of
// the identical error so one persistently failing route does not spam the log
// on every route-watch push. A nil error (a later apply succeeded) re-arms
// the log so the next distinct failure is reported again.
func (s *Server) warnApplyFailure(msg string, err error) {
	s.applyErrMu.Lock()
	defer s.applyErrMu.Unlock()
	if err == nil {
		s.applyErrLogged = false
		s.lastApplyErr = ""
		return
	}
	if s.applyErrLogged && s.lastApplyErr == err.Error() {
		return
	}
	s.applyErrLogged = true
	s.lastApplyErr = err.Error()
	s.logger.Warn(msg, zap.Error(err))
}

// watchStaleAutoRoutes flushes auto-installed routes and mesh DNS state when
// no route-watch snapshot has arrived within timeout. It runs only when
// autoroutes.stale_timeout is configured; by default auto routes are kept
// through agent flaps indefinitely.
func (s *Server) watchStaleAutoRoutes(ctx context.Context, w *autoroutes.Watcher, timeout time.Duration) {
	interval := timeout / 4
	if interval < time.Second {
		interval = time.Second
	}
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	flushed := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		last := w.LastSnapshot()
		if last.IsZero() {
			continue // no snapshot ever arrived; there is nothing to flush
		}
		if time.Since(last) <= timeout {
			flushed = false // fresh snapshot: re-arm
			continue
		}
		if flushed {
			continue
		}
		flushed = true
		s.logger.Warn("no route-watch snapshot within autoroutes.stale_timeout; flushing auto routes and mesh DNS state",
			zap.Duration("stale_timeout", timeout),
			zap.Time("last_snapshot", last),
		)
		s.flushAutoRoutes()
	}
}

// flushAutoRoutes drops all auto-installed routes and clears the mesh DNS
// state (suffixes + hosts), leaving config routes in place. The next
// route-watch snapshot re-installs everything via onDashboardUpdate.
func (s *Server) flushAutoRoutes() {
	s.routeState.setAutoRoutes(nil)
	s.warnApplyFailure("failed to flush stale autoroutes", s.syncRoutes())

	dnsServer, _, _ := s.dnsState()
	if dnsServer == nil {
		return
	}
	s.applyDNSIntegration(dnsServer.SetState(nil, nil))
}

// filterOutDests returns routes whose destination string is not in skip.
func filterOutDests(routes []route.Route, skip map[string]bool) []route.Route {
	if len(skip) == 0 {
		return routes
	}
	out := routes[:0]
	for _, r := range routes {
		if r.Destination != nil && skip[r.Destination.String()] {
			continue
		}
		out = append(out, r)
	}
	return out
}

// selfRouteSet returns the interface's own address routes (the TUN
// IPv4 /32 and IPv6 /128) that the kernel manages and re-creates.
func (s *Server) selfRouteSet() map[string]bool {
	cfg := s.cfg.Load()
	set := make(map[string]bool, 2)
	if ip, _, err := cfg.GetTUNIPv4(); err == nil && ip != nil {
		set[ip.String()+"/32"] = true
	}
	if ip, _, err := cfg.GetTUNIPv6(); err == nil && ip != nil {
		set[ip.String()+"/128"] = true
	}
	return set
}

// Reload applies a new configuration
func (s *Server) Reload(cfg *config.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldCfg := s.cfg.Load()
	if s.socks5ConfigChanged(oldCfg, cfg) {
		s.network.socks5Client = s.createSOCKS5Client(cfg)
		s.network.tcpForwarder.UpdateClient(s.network.socks5Client)
		s.network.rawUDPForwarder.UpdateClient(s.network.socks5Client)
		s.network.icmpForwarder.UpdateClient(s.network.socks5Client)
		s.logger.Info("SOCKS5 client updated", zap.String("server", cfg.SOCKS5.Server))

		// The autoroutes watcher pins its SOCKS5 client at construction;
		// restart it so it reconnects to the new endpoint instead of the
		// old one forever.
		if cfg.AutoRoutes.Enabled {
			s.servicesMu.Lock()
			svcCtx := s.servicesCtx
			s.servicesMu.Unlock()
			if svcCtx != nil {
				s.startAutoRoutes(svcCtx)
			}
		}
	}

	s.cfg.Store(cfg)

	// Re-resolve the protected SOCKS5 server address in case it changed, so
	// syncRoutes refuses routes covering the new endpoint.
	s.refreshProtectedServerIPs()

	if err := s.syncRoutes(); err != nil {
		s.warnApplyFailure("failed to reapply routes", err)
	}

	if s.exposeMgr != nil {
		if err := s.exposeMgr.Reload(cfg.Expose, s.network.socks5Client); err != nil {
			s.logger.Warn("failed to reload expose", zap.Error(err))
		}
	}

	// Sections Reload cannot apply live: warn so the operator knows a
	// restart is required instead of assuming the change took effect.
	if sections := restartRequiredChanges(oldCfg, cfg); len(sections) > 0 {
		s.logger.Warn("config reloaded, but changes to these sections require a daemon restart to take effect",
			zap.Strings("sections", sections),
		)
	}

	return nil
}

// restartRequiredChanges returns the names of config sections that changed
// between oldCfg and newCfg but are only applied at daemon startup: tun.*,
// admin.*, dns_integration, autoroutes.*, and logging.*.
func restartRequiredChanges(oldCfg, newCfg *config.Config) []string {
	var changed []string
	if !reflect.DeepEqual(oldCfg.TUN, newCfg.TUN) {
		changed = append(changed, "tun")
	}
	if !reflect.DeepEqual(oldCfg.Admin, newCfg.Admin) {
		changed = append(changed, "admin")
	}
	if oldCfg.DNSIntegration != newCfg.DNSIntegration {
		changed = append(changed, "dns_integration")
	}
	if !reflect.DeepEqual(oldCfg.AutoRoutes, newCfg.AutoRoutes) {
		changed = append(changed, "autoroutes")
	}
	if !reflect.DeepEqual(oldCfg.Logging, newCfg.Logging) {
		changed = append(changed, "logging")
	}
	return changed
}

// socks5ConfigChanged checks if SOCKS5 configuration has changed
func (s *Server) socks5ConfigChanged(oldCfg, newCfg *config.Config) bool {
	return newCfg.SOCKS5.Server != oldCfg.SOCKS5.Server ||
		newCfg.SOCKS5.Username != oldCfg.SOCKS5.Username ||
		newCfg.SOCKS5.Password != oldCfg.SOCKS5.Password ||
		newCfg.SOCKS5.Timeout != oldCfg.SOCKS5.Timeout ||
		newCfg.SOCKS5.KeepAlive != oldCfg.SOCKS5.KeepAlive ||
		newCfg.SOCKS5.Transport != oldCfg.SOCKS5.Transport ||
		newCfg.SOCKS5.WSPath != oldCfg.SOCKS5.WSPath
}

// Stop stops the daemon server
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancelFn != nil {
		s.cancelFn()
	}
	return nil
}

// ReloadFromDisk reloads the config from the path the daemon was started
// with, validates it, and applies it. The full load+validate happens
// before any state is mutated, so a malformed config leaves the running
// daemon untouched. This is the control-plane entry point used by the
// daemon.reload API method (the cross-platform replacement for SIGHUP).
func (s *Server) ReloadFromDisk() error {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		return fmt.Errorf("failed to reload config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("refusing to reload invalid config: %w", err)
	}
	return s.Reload(cfg)
}

// ReloadConfig implements the api.StateProvider control-plane hook;
// it reloads and applies the on-disk config.
func (s *Server) ReloadConfig() error { return s.ReloadFromDisk() }

// RequestStop implements the api.StateProvider control-plane hook; it
// triggers a graceful shutdown (context cancellation), which unwinds
// Run's deferred teardown (PID file removal, TUN/route/DNS cleanup).
func (s *Server) RequestStop() error { return s.Stop() }

// removePIDFile removes the PID file
func (s *Server) removePIDFile() {
	os.Remove(s.cfg.Load().Daemon.PIDFile)
}

// ReadPIDFile reads the PID from a file
func ReadPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

// StateProvider interface implementation for API server

// GetStatus returns the daemon status
func (s *Server) GetStatus() *api.StatusResult {
	s.mu.RLock()
	running := s.running
	startTime := s.startTime
	s.mu.RUnlock()

	cfg := s.cfg.Load()
	autoRouteCount := s.routeState.autoRouteCount()

	result := &api.StatusResult{
		Running:      running,
		PID:          os.Getpid(),
		ConfigPath:   s.configPath,
		TUNName:      s.ifaceName(),
		TUNAddress:   cfg.TUN.Address,
		SOCKS5Server: cfg.SOCKS5.Server,
		SOCKS5Status: "configured",
	}

	if running {
		result.Uptime = time.Since(startTime).Round(time.Second).String()
	}

	result.AutoRoutes.Enabled = cfg.AutoRoutes.Enabled
	if cfg.AutoRoutes.Enabled {
		result.AutoRoutes.Count = autoRouteCount
		if ts := s.lastRouteSnapshot(); !ts.IsZero() {
			result.AutoRoutes.LastSnapshot = ts
			result.AutoRoutes.LastSnapshotAge = time.Since(ts).Round(time.Second).String()
		}
	}

	result.RouteCount.Config = len(cfg.Routes)
	result.RouteCount.Auto = autoRouteCount
	result.RouteCount.Total = result.RouteCount.Config + result.RouteCount.Auto

	return result
}

// lastRouteSnapshot returns when the running autoroutes watcher last applied
// a route-watch snapshot, or the zero time when no watcher is running or no
// snapshot has arrived yet.
func (s *Server) lastRouteSnapshot() time.Time {
	s.servicesMu.Lock()
	h := s.watcher
	s.servicesMu.Unlock()
	if h == nil || h.watcher == nil {
		return time.Time{}
	}
	return h.watcher.LastSnapshot()
}

// GetRoutes returns the list of active routes with source info. Routes refused
// for covering the SOCKS5 server are omitted here (they are reported separately
// via GetRefusedRoutes) so the same destination never appears as both applied
// and refused.
func (s *Server) GetRoutes() []api.RouteInfo {
	cfg := s.cfg.Load()
	refused := s.refusedDestSet()
	var routes []api.RouteInfo

	for _, r := range cfg.Routes {
		// Refused destinations are stored canonicalized; match on the same
		// form so a non-canonical config entry is still recognised.
		key := r.Destination
		if _, ipNet, err := net.ParseCIDR(r.Destination); err == nil {
			key = ipNet.String()
		}
		if refused[key] {
			continue
		}
		routes = append(routes, api.RouteInfo{
			Destination: r.Destination,
			Interface:   s.ifaceName(),
			Comment:     r.Comment,
			Source:      "config",
			Enabled:     r.Enabled,
		})
	}

	for _, r := range s.routeState.getAutoRoutes() {
		dest := r.Destination.String()
		if refused[dest] {
			continue
		}
		routes = append(routes, api.RouteInfo{
			Destination: dest,
			Interface:   s.ifaceName(),
			Comment:     r.Comment,
			Source:      "auto",
			Enabled:     true,
		})
	}

	return routes
}

// GetRouteEvents returns the recent route-change log (oldest first).
func (s *Server) GetRouteEvents() *api.RouteEventsResult {
	return &api.RouteEventsResult{Events: s.routeEvents.snapshot()}
}

// AddRoute adds a route and optionally persists to config
func (s *Server) AddRoute(dest, comment string, persist bool) error {
	cfg := s.cfg.Load()

	_, ipNet, err := net.ParseCIDR(dest)
	if err != nil {
		return fmt.Errorf("invalid CIDR: %w", err)
	}

	r := route.Route{
		Destination: ipNet,
		Interface:   s.ifaceName(),
		Comment:     comment,
		Enabled:     true,
	}

	if err := s.routeMgr.Add(r); err != nil {
		return fmt.Errorf("failed to add route: %w", err)
	}
	s.routeEvents.record("added", ipNet.String(), "manual", comment)

	if persist {
		next := configWithRouteAdded(cfg, dest, comment)
		s.cfg.Store(next)
		if err := next.Save(s.configPath); err != nil {
			return fmt.Errorf("route added but failed to save config: %w", err)
		}
	}

	return nil
}

// configWithRouteAdded returns a copy of cfg with the route appended. The
// input is never mutated: the stored config pointer is shared with
// concurrent readers (GetRoutes, GetStatus), so in-place append/splice
// would race with them.
func configWithRouteAdded(cfg *config.Config, dest, comment string) *config.Config {
	next := *cfg
	next.Routes = append(slices.Clone(cfg.Routes), config.RouteConfig{
		Destination: dest,
		Comment:     comment,
		Enabled:     true,
	})
	return &next
}

// configWithRouteRemoved returns a copy of cfg with the first route matching
// dest dropped. The input is never mutated (see configWithRouteAdded).
func configWithRouteRemoved(cfg *config.Config, dest string) *config.Config {
	next := *cfg
	next.Routes = slices.Clone(cfg.Routes)
	for i, rc := range next.Routes {
		if rc.Destination == dest {
			next.Routes = append(next.Routes[:i], next.Routes[i+1:]...)
			break
		}
	}
	return &next
}

// RemoveRoute removes a route and optionally persists to config
func (s *Server) RemoveRoute(dest string, persist bool) error {
	_, ipNet, err := net.ParseCIDR(dest)
	if err != nil {
		return fmt.Errorf("invalid CIDR: %w", err)
	}

	r := route.Route{
		Destination: ipNet,
	}

	if err := s.routeMgr.Remove(r); err != nil {
		return fmt.Errorf("failed to remove route: %w", err)
	}
	s.routeEvents.record("removed", ipNet.String(), "manual", "")

	if persist {
		cfg := s.cfg.Load()
		next := configWithRouteRemoved(cfg, dest)
		s.cfg.Store(next)
		if err := next.Save(s.configPath); err != nil {
			return fmt.Errorf("route removed but failed to save config: %w", err)
		}
	}

	return nil
}

// TraceRoute traces routing for a destination. The destination may
// be a literal IP, a mesh hostname (resolved via the in-memory
// registry when the name ends with one of the active mesh suffixes),
// or a regular hostname (resolved via the system resolver). The
// Resolution field on the result records which path was used.
func (s *Server) TraceRoute(dest string) (*api.RouteTraceResult, error) {
	cfg := s.cfg.Load()

	resolvedIP, source, err := s.resolveTraceTarget(dest, cfg)
	if err != nil {
		return nil, err
	}

	lookup, err := route.Lookup(resolvedIP, s.ifaceName())
	if err != nil {
		return nil, err
	}

	result := &api.RouteTraceResult{
		Destination:  dest,
		Resolution:   source,
		ResolvedIP:   resolvedIP.String(),
		MatchedRoute: lookup.MatchedRoute.String(),
		Interface:    lookup.Interface,
		IsMutiauk:    lookup.IsMutiauk,
	}

	if lookup.Gateway != nil && !lookup.Gateway.IsUnspecified() {
		result.Gateway = lookup.Gateway.String()
	}

	if lookup.IsMutiauk {
		if path, err := s.lookupMeshPath(context.Background(), resolvedIP); err != nil {
			s.logger.Debug("failed to lookup mesh path", zap.Error(err))
		} else if path != nil {
			result.MeshPath = path.PathDisplay
			result.Origin = path.Origin
			result.OriginID = path.OriginID
			result.HopCount = path.HopCount
		}
	}

	return result, nil
}

// lookupMeshPath resolves the mesh path for ip from a one-shot SOCKS5 snapshot
// (CmdSnapshot) over the agent's SOCKS5 endpoint.
func (s *Server) lookupMeshPath(ctx context.Context, ip net.IP) (*autoroutes.PathResult, error) {
	snap, err := s.createSOCKS5Client(s.cfg.Load()).Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return autoroutes.LookupPathFromSnapshot(snap, ip)
}

// resolveTraceTarget turns a destination string into an IP plus a
// resolution-source label. Mesh-suffixed names hit the in-memory
// registry only; an unregistered name under an active mesh suffix
// returns an error rather than falling through to the system
// resolver, so misnamed hosts fail loudly.
func (s *Server) resolveTraceTarget(dest string, cfg *config.Config) (net.IP, string, error) {
	if ip := net.ParseIP(dest); ip != nil {
		return ip, api.ResolutionIP, nil
	}

	name := strings.ToLower(strings.TrimSuffix(dest, "."))

	dnsServer, _, _ := s.dnsState()
	if dnsServer != nil && meshdns.MatchesAnySuffix(name, dnsServer.Suffixes()) {
		h, ok := dnsServer.Lookup(name)
		if !ok {
			return nil, "", fmt.Errorf("mesh hostname %q is not registered", dest)
		}
		if h.IPv4 != nil {
			return h.IPv4, api.ResolutionMesh, nil
		}
		if h.IPv6 != nil {
			return h.IPv6, api.ResolutionMesh, nil
		}
		return nil, "", fmt.Errorf("mesh hostname %q has no IP", dest)
	}

	ips, err := net.LookupIP(name)
	if err != nil {
		return nil, "", fmt.Errorf("failed to resolve %s: %w", dest, err)
	}
	if len(ips) == 0 {
		return nil, "", fmt.Errorf("no IP addresses for %s", dest)
	}
	for _, addr := range ips {
		if addr.To4() != nil {
			return addr, api.ResolutionSystem, nil
		}
	}
	return ips[0], api.ResolutionSystem, nil
}

// GetConfigPath returns the config file path
func (s *Server) GetConfigPath() string {
	return s.configPath
}

// GetMeshHosts returns the current mesh hostname map served by the
// local DNS resolver, alongside the active TLD suffix set. Enabled
// is false when the resolver is not running (e.g. autoroutes is
// disabled); in that case Hosts and Suffixes are empty.
func (s *Server) GetMeshHosts() *api.HostListResult {
	dnsServer, _, _ := s.dnsState()
	if dnsServer == nil {
		return &api.HostListResult{Enabled: false}
	}

	result := &api.HostListResult{
		Enabled:  true,
		Suffixes: dnsServer.Suffixes(),
	}

	hosts := dnsServer.Hosts()
	result.Hosts = make([]api.MeshHostInfo, 0, len(hosts))
	for _, h := range hosts {
		info := api.MeshHostInfo{
			Name:    h.Name,
			Comment: h.Comment,
		}
		if h.IPv4 != nil {
			info.IPv4 = h.IPv4.String()
		}
		if h.IPv6 != nil {
			info.IPv6 = h.IPv6.String()
		}
		result.Hosts = append(result.Hosts, info)
	}
	return result
}

// GetExposeStatus returns a snapshot of the expose manager's current
// state for the API server. When the manager is not running (expose
// disabled), returns a zero-valued result with Enabled=false.
func (s *Server) GetExposeStatus() *api.ExposeStatusResult {
	if s.exposeMgr == nil {
		return &api.ExposeStatusResult{Enabled: false}
	}
	st := s.exposeMgr.Status()
	out := &api.ExposeStatusResult{
		Enabled:          st.Enabled,
		Connected:        st.Connected,
		LastError:        st.LastError,
		ConnectedSince:   st.ConnectedSince,
		MaxStreamsPerKey: st.MaxStreamsPerKey,
	}
	out.Forwards = make([]api.ExposeForwardStatus, 0, len(st.Forwards))
	for _, f := range st.Forwards {
		out.Forwards = append(out.Forwards, api.ExposeForwardStatus{
			Key:           f.Key,
			Type:          f.Type,
			Source:        f.Source,
			Path:          f.Path,
			Target:        f.Target,
			Comment:       f.Comment,
			Registered:    f.Registered,
			Error:         f.Error,
			ActiveStreams: f.ActiveStreams,
		})
	}
	return out
}

// AddExpose installs a runtime forward and registers it over the
// active websocket session (if connected). Returns an error if the
// expose manager is not running, the input is invalid, or the key is
// already in use.
func (s *Server) AddExpose(key, ftype, path, target, comment string) error {
	if s.exposeMgr == nil {
		return fmt.Errorf("expose is not enabled")
	}
	return s.exposeMgr.Add(config.ForwardConfig{
		Key:     key,
		Type:    ftype,
		Path:    path,
		Target:  target,
		Comment: comment,
	})
}

// RemoveExpose drops a runtime-added forward. Errors if the key is
// unknown or config-defined.
func (s *Server) RemoveExpose(key string) error {
	if s.exposeMgr == nil {
		return fmt.Errorf("expose is not enabled")
	}
	return s.exposeMgr.Remove(key)
}

// GetDNSStatus returns the daemon's view of mesh-host DNS: the chosen
// host integration, the resolver's listen address, the active TLD
// set, and the last-applied diff-cache state. Used by
// `mutiauk dns status`.
func (s *Server) GetDNSStatus() *api.DNSStatusResult {
	cfg := s.cfg.Load()
	out := &api.DNSStatusResult{
		Preference: cfg.DNSIntegration,
	}
	dnsServer, listenIP, integration := s.dnsState()
	if dnsServer == nil {
		out.Backend = meshdns.IntegrationNone
		return out
	}

	out.Enabled = true
	out.Backend = meshdns.IntegrationNone
	if integration != nil {
		out.Backend = integration.Name()
	}
	out.Listen = net.JoinHostPort(listenIP, "53")
	out.Suffixes = dnsServer.Suffixes()
	out.HostCount = dnsServer.HostCount()

	s.appliedMu.Lock()
	out.AppliedListen = s.applied.listenIP
	out.AppliedSuffixes = slices.Clone(s.applied.suffixes)
	out.AppliedAt = s.applied.at
	s.appliedMu.Unlock()
	return out
}
