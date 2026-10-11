// Package dns implements the local mesh DNS resolver.
//
// The resolver answers A and AAAA records for a fixed set of mesh
// hostnames (e.g. "target-11.mesh") served from an in-memory map
// populated by the autoroutes poller. The active TLD suffix set is
// also delivered by the poller (from the agent's
// mesh_hosts_suffixes); names outside the active set return NXDOMAIN.
// In production systemd-resolved is configured to forward only those
// suffixes here, so the resolver effectively never sees other names.
package dns

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	mdns "github.com/miekg/dns"
	"go.uber.org/zap"
)

// Host is a single mesh hostname mapping. Either IPv4 or IPv6 (or
// both) must be set. Name is stored as a lowercase FQDN ending in one
// of the active suffix labels.
type Host struct {
	Name    string
	IPv4    net.IP
	IPv6    net.IP
	Comment string
}

// Server is the local mesh DNS resolver.
type Server struct {
	ttl    uint32
	logger *zap.Logger

	mu       sync.RWMutex
	suffixes []string        // lowercase, no leading dot, sorted (for snapshot stability)
	hosts    map[string]Host // key: lowercase FQDN without trailing dot

	udp *mdns.Server
	tcp *mdns.Server
}

// Config holds DNS server configuration.
type Config struct {
	// TTL is the DNS record TTL in seconds. 0 means use a sensible
	// default.
	TTL uint32
	// Logger is required.
	Logger *zap.Logger
}

// New creates a DNS server. Until SetState is called the resolver
// answers NXDOMAIN to every query.
func New(cfg Config) (*Server, error) {
	if cfg.Logger == nil {
		return nil, fmt.Errorf("dns: logger is required")
	}
	ttl := cfg.TTL
	if ttl == 0 {
		ttl = 60
	}
	return &Server{
		ttl:    ttl,
		logger: cfg.Logger,
		hosts:  make(map[string]Host),
	}, nil
}

// SetState atomically replaces the active suffix set and the host
// map and returns the canonical (lowercased, sorted, deduplicated)
// suffix slice it now holds. Hosts whose name doesn't end in any
// active suffix are dropped with a warning.
func (s *Server) SetState(suffixes []string, hosts []Host) []string {
	cleanSuffixes := s.normalizeSuffixes(suffixes)

	next := make(map[string]Host, len(hosts))
	for _, h := range hosts {
		name := canonName(h.Name)
		if name == "" {
			continue
		}
		if !nameMatchesAny(name, cleanSuffixes) {
			s.logger.Warn("dropping mesh host outside active suffixes",
				zap.String("name", h.Name),
				zap.Strings("suffixes", cleanSuffixes),
			)
			continue
		}
		h.Name = name
		next[name] = h
	}

	s.mu.Lock()
	s.suffixes = cleanSuffixes
	s.hosts = next
	s.mu.Unlock()

	s.logger.Debug("mesh state updated",
		zap.Int("hosts", len(next)),
		zap.Strings("suffixes", cleanSuffixes),
	)
	return cleanSuffixes
}

// canonName lowercases an FQDN and strips a trailing dot.
func canonName(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

// normalizeSuffixes lowercases, trims surrounding whitespace and
// dots, drops empty entries, and returns a sorted, deduplicated
// slice. Multi-label suffixes (e.g. "muti-metroo.alt") are preserved.
// Entries that are not valid DNS names are dropped with a warning:
// suffixes arrive from the Muti Metroo agent and are written into
// root-owned system configuration (dnsmasq drop-ins, /etc/resolver
// files, resolvectl argv, NRPT), so anything outside the strict DNS
// charset is a config-injection or path-traversal vector.
func (s *Server) normalizeSuffixes(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, suf := range in {
		v := strings.ToLower(strings.Trim(strings.TrimSpace(suf), "."))
		if v == "" {
			continue
		}
		if !isValidSuffix(v) {
			s.logger.Warn("dropping invalid mesh DNS suffix",
				zap.String("suffix", suf),
			)
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

// isValidSuffix reports whether s is a syntactically valid DNS suffix:
// at most 253 chars, built from non-empty labels of at most 63 chars
// each, every label lowercase alphanumeric with interior hyphens only
// (no leading/trailing hyphen). An empty label rejects "..", a leading
// or trailing dot, and any path separator or whitespace automatically.
func isValidSuffix(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if !isValidDNSLabel(label) {
			return false
		}
	}
	return true
}

// isValidDNSLabel reports whether label matches [a-z0-9] with interior
// hyphens allowed, 1-63 chars.
func isValidDNSLabel(label string) bool {
	if label == "" || len(label) > 63 {
		return false
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// HostCount returns the number of registered mesh hosts.
func (s *Server) HostCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.hosts)
}

// Suffixes returns a copy of the active suffix set.
func (s *Server) Suffixes() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.suffixes)
}

// Lookup returns the registered host with the given name. The match
// is case-insensitive and a trailing dot is tolerated. The bool is
// false when the name is not registered.
func (s *Server) Lookup(name string) (Host, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.hosts[canonName(name)]
	return h, ok
}

// Hosts returns a snapshot of the host map.
func (s *Server) Hosts() []Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Host, 0, len(s.hosts))
	for _, h := range s.hosts {
		out = append(out, h)
	}
	return out
}

// Listen starts UDP and TCP listeners on addr (host:port). It returns
// once both listeners are accepting traffic. Listeners are stopped
// when ctx is cancelled.
func (s *Server) Listen(ctx context.Context, addr string) error {
	mux := mdns.NewServeMux()
	mux.HandleFunc(".", s.handle)

	udpReady := make(chan error, 1)
	s.udp = &mdns.Server{
		Addr:              addr,
		Net:               "udp",
		Handler:           mux,
		NotifyStartedFunc: func() { udpReady <- nil },
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}
	go func() {
		if err := s.udp.ListenAndServe(); err != nil {
			udpReady <- fmt.Errorf("dns udp listener: %w", err)
		}
	}()

	tcpReady := make(chan error, 1)
	s.tcp = &mdns.Server{
		Addr:              addr,
		Net:               "tcp",
		Handler:           mux,
		NotifyStartedFunc: func() { tcpReady <- nil },
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}
	go func() {
		if err := s.tcp.ListenAndServe(); err != nil {
			tcpReady <- fmt.Errorf("dns tcp listener: %w", err)
		}
	}()

	// Collect the startup outcome of both listeners. On any failure or
	// cancellation, wait for BOTH goroutines to settle before Shutdown:
	// miekg/dns ignores Shutdown for a server that has not bound yet, so
	// tearing down early leaks whichever listener is still starting.
	var udpErr, tcpErr error
	udpGot, tcpGot := false, false
	select {
	case udpErr = <-udpReady:
		udpGot = true
	case <-ctx.Done():
		udpErr = ctx.Err()
	}
	if udpErr == nil {
		select {
		case tcpErr = <-tcpReady:
			tcpGot = true
		case <-ctx.Done():
			tcpErr = ctx.Err()
		}
	}
	if udpErr != nil || tcpErr != nil {
		if !udpGot {
			<-udpReady
		}
		if !tcpGot {
			<-tcpReady
		}
		s.shutdown()
		if udpErr != nil {
			return udpErr
		}
		return tcpErr
	}

	s.logger.Info("dns server listening", zap.String("addr", addr))

	go func() {
		<-ctx.Done()
		s.shutdown()
	}()

	return nil
}

func (s *Server) shutdown() {
	if s.udp != nil {
		_ = s.udp.Shutdown()
	}
	if s.tcp != nil {
		_ = s.tcp.Shutdown()
	}
	s.logger.Debug("dns server stopped")
}

// handle answers a single DNS query. Anything that isn't an A/AAAA
// query for a name in an active suffix returns NXDOMAIN (or REFUSED
// for non-query opcodes).
func (s *Server) handle(w mdns.ResponseWriter, req *mdns.Msg) {
	resp := new(mdns.Msg)
	resp.SetReply(req)
	resp.Authoritative = true
	resp.RecursionAvailable = false

	if req.Opcode != mdns.OpcodeQuery {
		resp.Rcode = mdns.RcodeRefused
		_ = w.WriteMsg(resp)
		return
	}
	if len(req.Question) == 0 {
		resp.Rcode = mdns.RcodeFormatError
		_ = w.WriteMsg(resp)
		return
	}

	q := req.Question[0]
	name := canonName(q.Name)

	s.mu.RLock()
	suffixes := s.suffixes
	host, ok := s.hosts[name]
	s.mu.RUnlock()

	if !nameMatchesAny(name, suffixes) {
		resp.Rcode = mdns.RcodeNameError // NXDOMAIN
		_ = w.WriteMsg(resp)
		return
	}

	if !ok {
		resp.Rcode = mdns.RcodeNameError
		_ = w.WriteMsg(resp)
		return
	}

	switch q.Qtype {
	case mdns.TypeA:
		if host.IPv4 != nil {
			resp.Answer = append(resp.Answer, &mdns.A{
				Hdr: mdns.RR_Header{
					Name:   q.Name,
					Rrtype: mdns.TypeA,
					Class:  mdns.ClassINET,
					Ttl:    s.ttl,
				},
				A: host.IPv4,
			})
		}
	case mdns.TypeAAAA:
		if host.IPv6 != nil {
			resp.Answer = append(resp.Answer, &mdns.AAAA{
				Hdr: mdns.RR_Header{
					Name:   q.Name,
					Rrtype: mdns.TypeAAAA,
					Class:  mdns.ClassINET,
					Ttl:    s.ttl,
				},
				AAAA: host.IPv6,
			})
		}
	default:
		// Other RR types: NOERROR, empty answer (the name exists,
		// just not of this type).
	}

	_ = w.WriteMsg(resp)
}

// MatchesAnySuffix reports whether name belongs under any of the given
// mesh DNS suffixes. The match is case-insensitive and tolerates a
// trailing dot on name. Both the bare suffix ("mesh") and any strict
// subdomain ("foo.mesh") match. Empty suffixes is always false.
func MatchesAnySuffix(name string, suffixes []string) bool {
	return nameMatchesAny(canonName(name), suffixes)
}

// nameMatchesAny expects name already lowercased and trailing-dot
// stripped, and suffixes already normalised.
func nameMatchesAny(name string, suffixes []string) bool {
	if name == "" {
		return false
	}
	for _, suf := range suffixes {
		if suf == "" {
			continue
		}
		if name == suf || strings.HasSuffix(name, "."+suf) {
			return true
		}
	}
	return false
}
