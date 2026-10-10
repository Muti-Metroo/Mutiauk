package api

import (
	"encoding/json"
	"time"
)

// Request represents a JSON-RPC style request
type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	ID     int             `json:"id"`
}

// Response represents a JSON-RPC style response
type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
	ID     int             `json:"id"`
}

// Error represents an API error
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Error codes
const (
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternal       = -32603
)

// StatusResult contains daemon status information
type StatusResult struct {
	Running      bool             `json:"running"`
	PID          int              `json:"pid"`
	ConfigPath   string           `json:"config_path"`
	Uptime       string           `json:"uptime"`
	TUNName      string           `json:"tun_name"`
	TUNAddress   string           `json:"tun_address"`
	SOCKS5Server string           `json:"socks5_server"`
	SOCKS5Status string           `json:"socks5_status"`
	AutoRoutes   AutoRoutesStatus `json:"autoroutes"`
	RouteCount   RouteCountStatus `json:"route_count"`
}

// AutoRoutesStatus contains autoroutes status
type AutoRoutesStatus struct {
	Enabled  bool   `json:"enabled"`
	Count    int    `json:"count"`
	LastPoll string `json:"last_poll,omitempty"`
}

// RouteCountStatus contains route counts by source
type RouteCountStatus struct {
	Config int `json:"config"`
	Auto   int `json:"auto"`
	Manual int `json:"manual"`
	Total  int `json:"total"`
}

// RouteInfo represents a route with source information
type RouteInfo struct {
	Destination string `json:"destination"`
	Interface   string `json:"interface"`
	Gateway     string `json:"gateway,omitempty"`
	Comment     string `json:"comment,omitempty"`
	Source      string `json:"source"` // "config", "auto", "manual"
	Enabled     bool   `json:"enabled"`
}

// RouteAddParams contains parameters for route.add
type RouteAddParams struct {
	Destination string `json:"destination"`
	Comment     string `json:"comment,omitempty"`
	Persist     bool   `json:"persist"`
}

// RouteRemoveParams contains parameters for route.remove
type RouteRemoveParams struct {
	Destination string `json:"destination"`
	Persist     bool   `json:"persist"`
}

// RouteTraceParams contains parameters for route.trace
type RouteTraceParams struct {
	Destination string `json:"destination"`
}

// Resolution sources reported by RouteTraceResult.Resolution. The
// daemon writes one of these; the CLI compares against them.
const (
	ResolutionIP     = "ip"
	ResolutionMesh   = "mesh"
	ResolutionSystem = "system"
)

// RouteTraceResult contains trace results
type RouteTraceResult struct {
	Destination string `json:"destination"`
	// Resolution is one of ResolutionIP / ResolutionMesh /
	// ResolutionSystem, identifying how the destination was turned
	// into an IP.
	Resolution   string   `json:"resolution,omitempty"`
	ResolvedIP   string   `json:"resolved_ip"`
	MatchedRoute string   `json:"matched_route"`
	Interface    string   `json:"interface"`
	Gateway      string   `json:"gateway,omitempty"`
	IsMutiauk    bool     `json:"is_mutiauk"`
	MeshPath     []string `json:"mesh_path,omitempty"`
	Origin       string   `json:"origin,omitempty"`
	OriginID     string   `json:"origin_id,omitempty"`
	HopCount     int      `json:"hop_count,omitempty"`
}

// RouteListResult contains route list
type RouteListResult struct {
	Routes  []RouteInfo    `json:"routes"`
	Refused []RefusedRoute `json:"refused,omitempty"`
}

// RefusedRoute is a desired route the daemon declined to install because its
// CIDR covers the upstream SOCKS5 server's own address. Installing it would
// route the daemon's connection to its SOCKS server through the tunnel and
// loop, breaking all networking. Surfaced on the API/dashboard so operators
// running as a service (who never see the logs) can spot the misconfiguration.
type RefusedRoute struct {
	Destination string `json:"destination"`
	Source      string `json:"source,omitempty"` // "config" | "auto" | "manual"
	Reason      string `json:"reason"`
}

// RouteEvent is a single entry in the route-change log: a route was
// added to or removed from the kernel routing table (driven by config
// load, an autoroutes poll/push, a manual mutation, or reload). Seq is
// a per-daemon monotonic counter so a poller can request only events it
// has not seen yet.
type RouteEvent struct {
	Seq         uint64    `json:"seq"`
	Time        time.Time `json:"time"`
	Action      string    `json:"action"` // "added" | "removed"
	Destination string    `json:"destination"`
	Source      string    `json:"source,omitempty"` // "config" | "auto" | "manual"
	Comment     string    `json:"comment,omitempty"`
}

// RouteEventsResult is the response for "route.events" / the admin
// /api/route-events endpoint: the recent route-change log, oldest first.
type RouteEventsResult struct {
	Events []RouteEvent `json:"events"`
}

// MeshHostInfo represents a mesh hostname mapping known to the daemon.
type MeshHostInfo struct {
	Name    string `json:"name"`
	IPv4    string `json:"ipv4,omitempty"`
	IPv6    string `json:"ipv6,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// HostListResult contains the mesh host list and the active TLD set
// the resolver answers for.
type HostListResult struct {
	Hosts    []MeshHostInfo `json:"hosts"`
	Enabled  bool           `json:"enabled"`
	Suffixes []string       `json:"suffixes,omitempty"`
}

// DNSStatusResult is a snapshot of the daemon's view of mesh-host DNS:
// what backend was selected, what suffixes are active, and what state
// the diff cache last applied. Consumed by `mutiauk dns status`.
type DNSStatusResult struct {
	// Enabled is true when the resolver listener is bound and an
	// integration has been chosen. False on hosts where autoroutes
	// is disabled.
	Enabled bool `json:"enabled"`
	// Backend identifies the host integration in use:
	// "resolvectl", "nm-dnsmasq", or "none".
	Backend string `json:"backend"`
	// Preference is the operator-selected dns_integration value
	// ("auto", "resolvectl", ...).
	Preference string `json:"preference"`
	// Listen is the resolver bind address (host:port).
	Listen string `json:"listen,omitempty"`
	// Suffixes is the active suffix set the resolver answers for.
	Suffixes []string `json:"suffixes,omitempty"`
	// HostCount is the number of mesh hosts in the in-memory map.
	HostCount int `json:"host_count"`
	// AppliedSuffixes is the suffix set the diff cache last
	// successfully pushed to the backend. Drift between this and
	// Suffixes signals an Apply failure.
	AppliedSuffixes []string `json:"applied_suffixes,omitempty"`
	// AppliedListen is the listen IP the diff cache was keyed on.
	AppliedListen string `json:"applied_listen,omitempty"`
	// AppliedAt is the timestamp of the last successful Apply.
	AppliedAt time.Time `json:"applied_at,omitempty"`
}

// ExposeStatusResult is a snapshot of the expose subsystem: the
// websocket connection state, the list of forwards (config-defined +
// runtime-added), and per-key registration status. Returned by the
// "expose.status" API method.
//
// MaxStreamsPerKey is the configured per-key concurrent-stream cap
// (the OPEN-refusal threshold). Surfaced so `expose status` can
// render "ActiveStreams 12 / 256" without the CLI re-parsing the
// config file.
type ExposeStatusResult struct {
	Enabled          bool                  `json:"enabled"`
	Connected        bool                  `json:"connected"`
	LastError        string                `json:"last_error,omitempty"`
	ConnectedSince   time.Time             `json:"connected_since,omitempty"`
	MaxStreamsPerKey int                   `json:"max_streams_per_key,omitempty"`
	Forwards         []ExposeForwardStatus `json:"forwards"`
}

// ExposeForwardStatus is one row of ExposeStatusResult.Forwards.
type ExposeForwardStatus struct {
	Key           string `json:"key"`
	Type          string `json:"type"`             // "folder" | "tcp"
	Source        string `json:"source"`           // "config" | "runtime"
	Path          string `json:"path,omitempty"`   // folder root
	Target        string `json:"target,omitempty"` // tcp host:port
	Comment       string `json:"comment,omitempty"`
	Registered    bool   `json:"registered"`
	Error         string `json:"error,omitempty"`
	ActiveStreams int    `json:"active_streams"`
}

// ExposeAddParams is the body of "expose.add". Path is set for
// type=folder, Target for type=tcp.
type ExposeAddParams struct {
	Key     string `json:"key"`
	Type    string `json:"type"`
	Path    string `json:"path,omitempty"`
	Target  string `json:"target,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// ExposeRemoveParams is the body of "expose.remove".
type ExposeRemoveParams struct {
	Key string `json:"key"`
}

// ExposeMutationResult is the response for "expose.add" /
// "expose.remove". When Success is false, Message contains the error
// surfaced to the operator.
type ExposeMutationResult struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// ReloadResult is the response for "daemon.reload". When Success is
// false, Message carries the reason the reload was rejected (e.g. the
// new config failed to load or validate); the running config is left
// untouched in that case.
type ReloadResult struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// StopResult is the response for "daemon.stop". Success indicates the
// shutdown was accepted; the daemon tears down shortly after the
// response is flushed.
type StopResult struct {
	Success bool `json:"success"`
}

// PingResult is the response for "daemon.ping": a cheap liveness probe
// that also returns the daemon PID. This is the authoritative liveness
// check across platforms (Windows has no signal-0 probe).
type PingResult struct {
	OK  bool `json:"ok"`
	PID int  `json:"pid"`
}

// RouteAddResult contains add result
type RouteAddResult struct {
	Success   bool   `json:"success"`
	Persisted bool   `json:"persisted"`
	Message   string `json:"message,omitempty"`
}

// RouteRemoveResult contains remove result
type RouteRemoveResult struct {
	Success   bool   `json:"success"`
	Persisted bool   `json:"persisted"`
	Message   string `json:"message,omitempty"`
}
