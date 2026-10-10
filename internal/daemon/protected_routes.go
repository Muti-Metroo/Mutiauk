package daemon

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/postalsys/mutiauk/internal/api"
	"github.com/postalsys/mutiauk/internal/route"
	"go.uber.org/zap"
)

// socksServerHost extracts the host portion of a SOCKS5 server address, which
// may be a "host:port" pair or a "ws://host:port/path" / "wss://..." URL.
func socksServerHost(server string) string {
	server = strings.TrimSpace(server)
	if strings.HasPrefix(server, "ws://") || strings.HasPrefix(server, "wss://") {
		if u, err := url.Parse(server); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	if host, _, err := net.SplitHostPort(server); err == nil {
		return host
	}
	return server
}

// resolveProtectedIPs resolves the SOCKS5 server host to the set of IPs that
// must never be routed into the TUN. A literal IP is used as-is; a hostname is
// resolved via the system resolver. Returns nil (and logs) when the host can't
// be resolved -- the common case is a literal IP, which never fails.
func resolveProtectedIPs(server string, logger *zap.Logger) []net.IP {
	host := socksServerHost(server)
	if host == "" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		logger.Warn("could not resolve SOCKS5 server host for route protection; a poisoned route covering it would not be refused",
			zap.String("host", host), zap.Error(err))
		return nil
	}
	return ips
}

// containsProtectedIP returns the first protected IP that ipNet contains, or
// nil when none do.
func containsProtectedIP(ipNet *net.IPNet, protected []net.IP) net.IP {
	if ipNet == nil {
		return nil
	}
	for _, ip := range protected {
		if ipNet.Contains(ip) {
			return ip
		}
	}
	return nil
}

// splitProtectedRoutes partitions desired routes into those safe to install and
// those refused because their CIDR covers a protected SOCKS5 server IP. The
// refused entries carry a human reason and a source label for the API.
func splitProtectedRoutes(routes []route.Route, protected []net.IP, autoSet, configSet map[string]bool) (keep []route.Route, refused []api.RefusedRoute) {
	for _, r := range routes {
		if ip := containsProtectedIP(r.Destination, protected); ip != nil {
			dest := r.Destination.String()
			refused = append(refused, api.RefusedRoute{
				Destination: dest,
				Source:      classifyRoute(dest, r.Comment, autoSet, configSet),
				Reason:      fmt.Sprintf("covers the SOCKS5 server %s; routing it through the tunnel would loop and break all networking", ip),
			})
			continue
		}
		keep = append(keep, r)
	}
	return keep, refused
}

// refreshProtectedServerIPs re-resolves the SOCKS5 server address and caches the
// protected IP set. Called at startup and on reload.
func (s *Server) refreshProtectedServerIPs() {
	cfg := s.cfg.Load()
	ips := resolveProtectedIPs(cfg.SOCKS5.Server, s.logger)

	s.protectedMu.Lock()
	s.protectedIPs = ips
	s.protectedMu.Unlock()
}

// protectedServerIPs returns a copy of the cached protected SOCKS5 server IPs.
func (s *Server) protectedServerIPs() []net.IP {
	s.protectedMu.RLock()
	defer s.protectedMu.RUnlock()
	if len(s.protectedIPs) == 0 {
		return nil
	}
	out := make([]net.IP, len(s.protectedIPs))
	copy(out, s.protectedIPs)
	return out
}

// setRefusedRoutes records the current refused-route snapshot (for the API and
// dashboard) and logs a one-time WARN for each newly-refused destination so a
// persistent poison route does not spam the log on every route-watch push.
func (s *Server) setRefusedRoutes(refused []api.RefusedRoute) {
	s.refusedMu.Lock()
	defer s.refusedMu.Unlock()

	s.refused = refused

	current := make(map[string]bool, len(refused))
	for _, rr := range refused {
		current[rr.Destination] = true
		if !s.refusedLogged[rr.Destination] {
			s.logger.Warn("refusing route that covers the SOCKS5 server (would loop); not installing it",
				zap.String("destination", rr.Destination),
				zap.String("source", rr.Source),
				zap.String("reason", rr.Reason))
		}
	}
	s.refusedLogged = current
}

// refusedDestSet returns the set of destinations currently refused, so route
// listings can omit them from the "applied" set.
func (s *Server) refusedDestSet() map[string]bool {
	s.refusedMu.Lock()
	defer s.refusedMu.Unlock()
	if len(s.refused) == 0 {
		return nil
	}
	set := make(map[string]bool, len(s.refused))
	for _, rr := range s.refused {
		set[rr.Destination] = true
	}
	return set
}

// GetRefusedRoutes implements api.StateProvider: the routes most recently
// declined because they cover the SOCKS5 server.
func (s *Server) GetRefusedRoutes() []api.RefusedRoute {
	s.refusedMu.Lock()
	defer s.refusedMu.Unlock()
	if len(s.refused) == 0 {
		return nil
	}
	out := make([]api.RefusedRoute, len(s.refused))
	copy(out, s.refused)
	return out
}
