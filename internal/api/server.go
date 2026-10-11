package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"
)

// StateProvider interface for daemon to implement
type StateProvider interface {
	GetStatus() *StatusResult
	GetRoutes() []RouteInfo
	// GetRefusedRoutes returns desired routes the daemon declined to install
	// because they cover the SOCKS5 server (would loop).
	GetRefusedRoutes() []RefusedRoute
	GetRouteEvents() *RouteEventsResult
	AddRoute(dest, comment string, persist bool) error
	RemoveRoute(dest string, persist bool) error
	TraceRoute(dest string) (*RouteTraceResult, error)
	GetConfigPath() string
	GetMeshHosts() *HostListResult
	GetDNSStatus() *DNSStatusResult
	GetExposeStatus() *ExposeStatusResult
	AddExpose(key, ftype, path, target, comment string) error
	RemoveExpose(key string) error
	// ReloadConfig reloads and applies the on-disk config (SIGHUP
	// replacement). RequestStop triggers graceful shutdown.
	ReloadConfig() error
	RequestStop() error
}

// HandlerFunc is a function that handles an API method
type HandlerFunc func(params json.RawMessage) (any, error)

// Server is the Unix socket API server
type Server struct {
	socketPath string
	listener   net.Listener
	handlers   map[string]HandlerFunc
	state      StateProvider
	mu         sync.RWMutex
	logger     *zap.Logger
	wg         sync.WaitGroup
}

// NewServer creates a new API server
func NewServer(socketPath string, state StateProvider, logger *zap.Logger) *Server {
	s := &Server{
		socketPath: socketPath,
		handlers:   make(map[string]HandlerFunc),
		state:      state,
		logger:     logger,
	}
	s.registerHandlers()
	return s
}

// registerHandlers sets up all API method handlers
func (s *Server) registerHandlers() {
	s.handlers["status"] = s.handleStatus
	s.handlers["route.list"] = s.handleRouteList
	s.handlers["route.add"] = s.handleRouteAdd
	s.handlers["route.remove"] = s.handleRouteRemove
	s.handlers["route.trace"] = s.handleRouteTrace
	s.handlers["host.list"] = s.handleHostList
	s.handlers["dns.status"] = s.handleDNSStatus
	s.handlers["config.path"] = s.handleConfigPath
	s.handlers["expose.status"] = s.handleExposeStatus
	s.handlers["expose.add"] = s.handleExposeAdd
	s.handlers["expose.remove"] = s.handleExposeRemove
	s.handlers["daemon.reload"] = s.handleDaemonReload
	s.handlers["daemon.stop"] = s.handleDaemonStop
	s.handlers["daemon.ping"] = s.handleDaemonPing
}

// Start starts the API server. The control endpoint is a Unix-domain
// socket on Linux/macOS (mode 0600) and a named pipe on Windows
// (restricted to SYSTEM + Administrators); listenEndpoint hides the
// platform difference.
func (s *Server) Start(ctx context.Context) error {
	listener, err := listenEndpoint(s.socketPath, s.logger)
	if err != nil {
		return err
	}
	s.listener = listener

	s.logger.Info("API server started", zap.String("endpoint", s.socketPath))

	s.wg.Add(1)
	go s.acceptLoop(ctx)

	return nil
}

// Stop stops the API server
func (s *Server) Stop() {
	if s.listener != nil {
		s.listener.Close()
	}
	s.wg.Wait()
	removeEndpoint(s.socketPath)
	s.logger.Info("API server stopped")
}

// acceptLoop accepts incoming connections
func (s *Server) acceptLoop(ctx context.Context) {
	defer s.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, err := s.listener.Accept()
		if err != nil {
			// Listener closed by Stop(): exit even when ctx is not yet
			// cancelled, otherwise this loop spins on a dead listener and
			// Stop's wg.Wait() never returns (daemon teardown order runs
			// cleanup before the deferred context cancel).
			if errors.Is(err, net.ErrClosed) {
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
				s.logger.Debug("accept error", zap.Error(err))
				continue
			}
		}

		s.wg.Add(1)
		go s.handleConnection(ctx, conn)
	}
}

// handleConnection handles a single client connection
func (s *Server) handleConnection(ctx context.Context, conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	decoder := json.NewDecoder(conn)
	encoder := json.NewEncoder(conn)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		var req Request
		if err := decoder.Decode(&req); err != nil {
			if err == io.EOF {
				return
			}
			s.logger.Debug("decode error", zap.Error(err))
			return
		}

		resp := s.handleRequest(&req)
		if err := encoder.Encode(resp); err != nil {
			s.logger.Debug("encode error", zap.Error(err))
			return
		}
	}
}

// handleRequest processes a single request
func (s *Server) handleRequest(req *Request) *Response {
	s.mu.RLock()
	handler, ok := s.handlers[req.Method]
	s.mu.RUnlock()

	if !ok {
		return &Response{
			Error: &Error{
				Code:    ErrCodeMethodNotFound,
				Message: "method not found: " + req.Method,
			},
			ID: req.ID,
		}
	}

	result, err := handler(req.Params)
	if err != nil {
		return &Response{
			Error: &Error{
				Code:    ErrCodeInternal,
				Message: err.Error(),
			},
			ID: req.ID,
		}
	}

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return &Response{
			Error: &Error{
				Code:    ErrCodeInternal,
				Message: "failed to marshal result",
			},
			ID: req.ID,
		}
	}

	return &Response{
		Result: resultJSON,
		ID:     req.ID,
	}
}

// Handler implementations

func (s *Server) handleStatus(_ json.RawMessage) (any, error) {
	return s.state.GetStatus(), nil
}

func (s *Server) handleRouteList(_ json.RawMessage) (any, error) {
	return &RouteListResult{
		Routes:  s.state.GetRoutes(),
		Refused: s.state.GetRefusedRoutes(),
	}, nil
}

func (s *Server) handleRouteAdd(params json.RawMessage) (any, error) {
	var p RouteAddParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if err := s.state.AddRoute(p.Destination, p.Comment, p.Persist); err != nil {
		return &RouteAddResult{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	return &RouteAddResult{
		Success:   true,
		Persisted: p.Persist,
	}, nil
}

func (s *Server) handleRouteRemove(params json.RawMessage) (any, error) {
	var p RouteRemoveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if err := s.state.RemoveRoute(p.Destination, p.Persist); err != nil {
		return &RouteRemoveResult{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	return &RouteRemoveResult{
		Success:   true,
		Persisted: p.Persist,
	}, nil
}

func (s *Server) handleRouteTrace(params json.RawMessage) (any, error) {
	var p RouteTraceParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	return s.state.TraceRoute(p.Destination)
}

func (s *Server) handleConfigPath(_ json.RawMessage) (any, error) {
	return map[string]string{"path": s.state.GetConfigPath()}, nil
}

func (s *Server) handleHostList(_ json.RawMessage) (any, error) {
	return s.state.GetMeshHosts(), nil
}

func (s *Server) handleDNSStatus(_ json.RawMessage) (any, error) {
	return s.state.GetDNSStatus(), nil
}

func (s *Server) handleExposeStatus(_ json.RawMessage) (any, error) {
	return s.state.GetExposeStatus(), nil
}

func (s *Server) handleExposeAdd(params json.RawMessage) (any, error) {
	var p ExposeAddParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if err := s.state.AddExpose(p.Key, p.Type, p.Path, p.Target, p.Comment); err != nil {
		return &ExposeMutationResult{Success: false, Message: err.Error()}, nil
	}
	return &ExposeMutationResult{Success: true}, nil
}

func (s *Server) handleExposeRemove(params json.RawMessage) (any, error) {
	var p ExposeRemoveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if err := s.state.RemoveExpose(p.Key); err != nil {
		return &ExposeMutationResult{Success: false, Message: err.Error()}, nil
	}
	return &ExposeMutationResult{Success: true}, nil
}

func (s *Server) handleDaemonReload(_ json.RawMessage) (any, error) {
	if err := s.state.ReloadConfig(); err != nil {
		return &ReloadResult{Success: false, Message: err.Error()}, nil
	}
	return &ReloadResult{Success: true}, nil
}

// handleDaemonStop accepts a graceful-shutdown request. The stop is
// scheduled on a short timer so this handler can return and the
// response gets flushed to the client before context cancellation tears
// down the listener; otherwise the client would see a connection reset
// instead of {success:true}.
func (s *Server) handleDaemonStop(_ json.RawMessage) (any, error) {
	time.AfterFunc(50*time.Millisecond, func() {
		if err := s.state.RequestStop(); err != nil {
			s.logger.Warn("daemon.stop request failed", zap.Error(err))
		}
	})
	return &StopResult{Success: true}, nil
}

func (s *Server) handleDaemonPing(_ json.RawMessage) (any, error) {
	return &PingResult{OK: true, PID: os.Getpid()}, nil
}
