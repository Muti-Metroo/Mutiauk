package autoroutes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/Muti-Metroo/Mutiauk/internal/backoff"
	"github.com/Muti-Metroo/Mutiauk/internal/route"
	"github.com/Muti-Metroo/Mutiauk/internal/socks5"
	"go.uber.org/zap"
)

// ErrUnsupported is returned by Watcher.Run when the agent does not support
// route-watch (the SOCKS5 server replied command-not-supported on the first
// attempt). The caller responds by disabling autoroutes.
var ErrUnsupported = errors.New("agent does not support route-watch over SOCKS")

// DashboardCallback is invoked once per applied route-watch snapshot with the
// filtered route set, the active mesh-host TLD suffixes, and the mesh host
// list. All three are delivered together so the daemon can apply them
// atomically: a suffix change without the matching host list (or vice versa)
// would otherwise drop hosts that arrived under a suffix the resolver doesn't
// yet know about.
type DashboardCallback func(routes []route.Route, suffixes []string, hosts []DashboardMeshHost)

const (
	watcherReconnectMin = 1 * time.Second
	watcherReconnectMax = 30 * time.Second
	// watcherReadTimeout is how long the watcher waits for any frame (snapshot
	// or heartbeat) before treating the connection as dead. The agent sends a
	// heartbeat every socks5.RouteWatchHeartbeatInterval.
	watcherReadTimeout = 2 * socks5.RouteWatchHeartbeatInterval
)

// Watcher receives dashboard snapshots pushed by the agent over a SOCKS5
// route-watch stream and applies each one through onUpdate. It reconnects
// with jittered exponential backoff.
type Watcher struct {
	client   *socks5.Client
	tunName  string
	onUpdate DashboardCallback
	logger   *zap.Logger

	// lastSnapshot is the UnixNano timestamp of the most recently applied
	// snapshot, or 0 when none has arrived. Used for status reporting and
	// the daemon's stale-route watchdog.
	lastSnapshot atomic.Int64
}

// NewWatcher creates a route-watch watcher. client must be a SOCKS5 client
// pointed at the agent's proxy endpoint.
func NewWatcher(client *socks5.Client, tunName string, onUpdate DashboardCallback, logger *zap.Logger) *Watcher {
	return &Watcher{
		client:   client,
		tunName:  tunName,
		onUpdate: onUpdate,
		logger:   logger,
	}
}

// LastSnapshot returns when the most recent route-watch snapshot was applied,
// or the zero time when none has arrived yet.
func (w *Watcher) LastSnapshot() time.Time {
	if ns := w.lastSnapshot.Load(); ns != 0 {
		return time.Unix(0, ns)
	}
	return time.Time{}
}

// Run opens a route-watch stream and applies pushed snapshots until ctx is
// cancelled. It returns ErrUnsupported if the agent rejects the route-watch
// command on the first attempt (the caller then disables autoroutes); other
// errors are handled internally with reconnect backoff and Run only returns on
// context cancellation.
func (w *Watcher) Run(ctx context.Context) error {
	backoffDur := watcherReconnectMin
	everConnected := false

	for {
		if ctx.Err() != nil {
			return nil
		}

		connected, err := w.session(ctx, &everConnected)
		if ctx.Err() != nil {
			return nil
		}

		// Unsupported on the very first attempt: signal the caller to disable autoroutes.
		if !everConnected && socks5.IsCommandNotSupported(err) {
			return ErrUnsupported
		}

		if connected {
			// A working session ended; reset backoff so a transient drop
			// reconnects promptly.
			backoffDur = watcherReconnectMin
		}
		if err != nil {
			w.logger.Warn("route-watch session ended",
				zap.Error(err), zap.Duration("retry_in", backoffDur))
		} else {
			w.logger.Debug("route-watch session ended; reconnecting",
				zap.Duration("retry_in", backoffDur))
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff.Jitter(backoffDur)):
		}
		backoffDur *= 2
		if backoffDur > watcherReconnectMax {
			backoffDur = watcherReconnectMax
		}
	}
}

// session runs one route-watch connection. It reports whether the stream
// produced at least one frame (connected) and the terminating error.
func (w *Watcher) session(ctx context.Context, everConnected *bool) (connected bool, err error) {
	stream, err := w.client.RouteWatch(ctx)
	if err != nil {
		return false, err
	}
	defer stream.Close()

	w.logger.Info("route-watch connected")

	for {
		if ctx.Err() != nil {
			return connected, nil
		}

		stream.SetReadDeadline(time.Now().Add(watcherReadTimeout))
		payload, err := stream.Next()
		if err != nil {
			return connected, err
		}

		connected = true
		*everConnected = true

		if payload == nil {
			// Heartbeat or unknown frame: liveness only.
			continue
		}

		if err := w.apply(payload); err != nil {
			w.logger.Warn("failed to apply route-watch snapshot", zap.Error(err))
		}
	}
}

// apply parses a snapshot, filters and conflict-resolves its routes, and
// delivers the result (plus the mesh-host suffixes and hosts) to onUpdate.
func (w *Watcher) apply(payload []byte) error {
	var dashboard DashboardResponse
	if err := json.Unmarshal(payload, &dashboard); err != nil {
		return fmt.Errorf("parse snapshot: %w", err)
	}

	routes := ResolveConflicts(FilterRoutes(dashboard.Routes, w.tunName))
	suffixes := dashboard.MeshHostsSuffixes
	hosts := dashboard.MeshHosts
	w.logger.Debug("applied route-watch snapshot",
		zap.Int("count", len(routes)),
		zap.Int("suffixes", len(suffixes)),
		zap.Int("hosts", len(hosts)),
	)
	if w.onUpdate != nil {
		w.onUpdate(routes, suffixes, hosts)
	}
	w.lastSnapshot.Store(time.Now().UnixNano())
	return nil
}
