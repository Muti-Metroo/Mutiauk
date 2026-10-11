package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"time"

	"github.com/Muti-Metroo/Mutiauk/internal/api"
	"github.com/Muti-Metroo/Mutiauk/internal/config"
	"github.com/Muti-Metroo/Mutiauk/internal/daemon"
	"github.com/Muti-Metroo/Mutiauk/internal/route"
)

// emitJSON writes v to w as 2-space-indented JSON. Centralised so
// every --json output across the CLI uses the same formatting.
func emitJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// formatTimestamp renders a time.Time in the project's standard
// human-output form: RFC3339 plus a parenthesized relative duration,
// e.g. "2026-05-05T14:22:01Z (32m ago)". A zero time prints empty so
// callers can suppress the label without per-site checks. Used by
// expose status, dns status, and daemon status to keep one
// vocabulary across the binary.
func formatTimestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return fmt.Sprintf("%s (%s ago)",
		t.UTC().Format(time.RFC3339),
		time.Since(t).Round(time.Second),
	)
}

// isDaemonRunning returns true when the daemon is responsive on its API
// socket, or when its PID file points at a live process. The second check
// catches stale-socket scenarios after an unclean shutdown.
func isDaemonRunning(cfg *config.Config) bool {
	if api.NewClient(cfg.Daemon.SocketPath).IsRunning() {
		return true
	}
	pid, err := daemon.ReadPIDFile(cfg.Daemon.PIDFile)
	if err != nil {
		return false
	}
	return pidAlive(pid)
}

// loadConfig loads the configuration file from the global cfgFile
// path. Permission-denied errors get a sudo hint because the config
// is written 0600 (it carries socks5.password) and the daemon's API
// socket is also 0600; every CLI command that
// talks to the daemon needs root.
func loadConfig() (*config.Config, error) {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		// errors.Is unwraps; os.IsPermission doesn't, and config.Load
		// wraps the os error with fmt.Errorf("%w").
		if errors.Is(err, fs.ErrPermission) {
			return nil, fmt.Errorf(
				"cannot read config %s: permission denied (file is 0600; rerun with sudo)",
				cfgFile,
			)
		}
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	return cfg, nil
}

// newRouteManager creates a route manager for the configured TUN interface.
func newRouteManager(cfg *config.Config) (*route.Manager, error) {
	mgr, err := route.NewManager(cfg.TUN.Name, GetLogger())
	if err != nil {
		return nil, fmt.Errorf("failed to create route manager: %w", err)
	}
	return mgr, nil
}

// configRoutesToRoutes converts config routes to route.Route slice,
// filtering only enabled routes.
func configRoutesToRoutes(cfg *config.Config) ([]route.Route, error) {
	var routes []route.Route
	for _, r := range cfg.Routes {
		if !r.Enabled {
			continue
		}
		_, ipNet, err := net.ParseCIDR(r.Destination)
		if err != nil {
			return nil, fmt.Errorf("invalid route in config: %s: %w", r.Destination, err)
		}
		routes = append(routes, route.Route{
			Destination: ipNet,
			Interface:   cfg.TUN.Name,
			Comment:     r.Comment,
			Enabled:     r.Enabled,
		})
	}
	return routes, nil
}

// configRoutesToRoutesAll converts all config routes to route.Route slice,
// including disabled routes.
func configRoutesToRoutesAll(cfg *config.Config) ([]route.Route, error) {
	var routes []route.Route
	for _, r := range cfg.Routes {
		_, ipNet, err := net.ParseCIDR(r.Destination)
		if err != nil {
			return nil, fmt.Errorf("invalid route in config: %s: %w", r.Destination, err)
		}
		routes = append(routes, route.Route{
			Destination: ipNet,
			Comment:     r.Comment,
			Enabled:     r.Enabled,
		})
	}
	return routes, nil
}

// DaemonAPIClient wraps the API client with helper methods for daemon interaction.
type DaemonAPIClient struct {
	client  *api.Client
	running bool
}

// newDaemonAPIClient creates a new daemon API client from the config.
func newDaemonAPIClient(cfg *config.Config) *DaemonAPIClient {
	client := api.NewClient(cfg.Daemon.SocketPath)
	return &DaemonAPIClient{
		client:  client,
		running: client.IsRunning(),
	}
}

// IsRunning returns true if the daemon is running and accessible.
func (d *DaemonAPIClient) IsRunning() bool {
	return d.running
}

// RouteAddResult contains the result of adding a route via daemon API.
type RouteAddResult struct {
	Success   bool
	Persisted bool
}

// AddRoute attempts to add a route via the daemon API.
// Returns nil, nil if the daemon is not running (caller should fall back to direct operation).
func (d *DaemonAPIClient) AddRoute(cidr, comment string, persist bool) (*RouteAddResult, error) {
	if !d.running {
		return nil, nil
	}
	result, err := d.client.RouteAdd(cidr, comment, persist)
	if err != nil {
		return nil, fmt.Errorf("failed to add route via daemon: %w", err)
	}
	if !result.Success {
		return nil, fmt.Errorf("failed to add route: %s", result.Message)
	}
	return &RouteAddResult{
		Success:   true,
		Persisted: result.Persisted,
	}, nil
}

// RouteRemoveResult contains the result of removing a route via daemon API.
type RouteRemoveResult struct {
	Success   bool
	Persisted bool
}

// RemoveRoute attempts to remove a route via the daemon API.
// Returns nil, nil if the daemon is not running (caller should fall back to direct operation).
func (d *DaemonAPIClient) RemoveRoute(cidr string, persist bool) (*RouteRemoveResult, error) {
	if !d.running {
		return nil, nil
	}
	result, err := d.client.RouteRemove(cidr, persist)
	if err != nil {
		return nil, fmt.Errorf("failed to remove route via daemon: %w", err)
	}
	if !result.Success {
		return nil, fmt.Errorf("failed to remove route: %s", result.Message)
	}
	return &RouteRemoveResult{
		Success:   true,
		Persisted: result.Persisted,
	}, nil
}

// ListRoutes fetches the route list from the daemon API, including source
// metadata. Returns nil, nil if the daemon is not running (caller should
// fall back to direct kernel listing).
func (d *DaemonAPIClient) ListRoutes() (*api.RouteListResult, error) {
	if !d.running {
		return nil, nil
	}
	result, err := d.client.RouteList()
	if err != nil {
		return nil, fmt.Errorf("failed to list routes via daemon: %w", err)
	}
	return result, nil
}

// TraceRoute traces a destination via the daemon API, which can
// consult the in-memory mesh-host registry. Returns nil, nil when
// the daemon is not running so the caller can fall back to a
// direct route lookup.
func (d *DaemonAPIClient) TraceRoute(destination string) (*api.RouteTraceResult, error) {
	if !d.running {
		return nil, nil
	}
	result, err := d.client.RouteTrace(destination)
	if err != nil {
		return nil, fmt.Errorf("failed to trace via daemon: %w", err)
	}
	return result, nil
}
