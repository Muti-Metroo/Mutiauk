// Package adminhttp serves the local admin interface for a running
// Mutiauk daemon: a self-contained browser dashboard plus JSON
// endpoints for inspecting state (applied routes, exposed
// folders/ports, Muti Metroo agent connection status) and managing local
// exposing.
//
// The interface is a thin HTTP layer over api.StateProvider (the same
// interface backing the Unix-socket control plane); it adds no daemon
// business logic. It is bound to loopback only and carries no
// authentication: the loopback binding is the trust boundary.
package adminhttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/postalsys/mutiauk/internal/api"
	"github.com/postalsys/mutiauk/internal/config"
)

// Server is the admin HTTP interface. It wraps an api.StateProvider and
// serves over one or two loopback listeners (IPv4 + IPv6 best-effort).
type Server struct {
	state  api.StateProvider
	cfg    config.AdminConfig
	logger *zap.Logger

	http      *http.Server
	listeners []net.Listener
	wg        sync.WaitGroup
}

// NewServer constructs an admin HTTP server over the given state
// provider. Call Start to bind and serve; Stop to tear down.
func NewServer(state api.StateProvider, cfg config.AdminConfig, logger *zap.Logger) *Server {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Server{state: state, cfg: cfg, logger: logger}
}

// Start binds the configured loopback address and serves the dashboard.
// It also best-effort binds the matching loopback of the other IP
// family on the same port, so the interface answers on both 127.0.0.1
// and ::1 regardless of which one the operator typed. Failure to bind
// the primary address is returned; failure to bind the secondary is
// logged and ignored.
func (s *Server) Start(_ context.Context) error {
	s.http = &http.Server{
		Handler: s.routes(),
		// Guard against Slowloris: a client that opens a connection
		// and dribbles headers cannot pin a goroutine indefinitely.
		ReadHeaderTimeout: 10 * time.Second,
	}

	primary, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	s.listeners = append(s.listeners, primary)
	s.serve(primary)

	// Best-effort bind the other loopback family on the same port.
	if secondary := s.secondaryListen(primary.Addr()); secondary != "" {
		if ln, err := net.Listen("tcp", secondary); err != nil {
			s.logger.Debug("admin: secondary loopback bind skipped",
				zap.String("addr", secondary), zap.Error(err))
		} else {
			s.listeners = append(s.listeners, ln)
			s.serve(ln)
		}
	}

	s.logger.Info("admin HTTP interface started", zap.String("listen", primary.Addr().String()))
	return nil
}

// serve runs http.Serve on ln in a tracked goroutine.
func (s *Server) serve(ln net.Listener) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Debug("admin: serve ended", zap.String("addr", ln.Addr().String()), zap.Error(err))
		}
	}()
}

// secondaryListen returns the host:port of the other loopback IP family
// for the primary address (::1 for an IPv4 primary, 127.0.0.1 for an
// IPv6 primary), or "" when no distinct second family applies (e.g. the
// operator bound a specific 127.0.0.x that is not the canonical one, or
// the address is already the partner family's canonical loopback).
func (s *Server) secondaryListen(primary net.Addr) string {
	host, port, err := net.SplitHostPort(primary.String())
	if err != nil {
		return ""
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	if ip.To4() != nil {
		return net.JoinHostPort("::1", port)
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// Stop gracefully shuts down the server and waits for serve goroutines.
func (s *Server) Stop() {
	if s.http != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.http.Shutdown(ctx)
	}
	for _, ln := range s.listeners {
		ln.Close()
	}
	s.wg.Wait()
	s.logger.Info("admin HTTP interface stopped")
}
