package autoroutes

import (
	"encoding/json"
	"fmt"
	"net"
)

// DashboardRoute represents a route from the Muti Metroo agent's route-watch snapshot.
type DashboardRoute struct {
	Network     string   `json:"network"`
	RouteType   string   `json:"route_type"` // "cidr" or "domain"
	Origin      string   `json:"origin"`
	OriginID    string   `json:"origin_id"`
	HopCount    int      `json:"hop_count"`
	PathDisplay []string `json:"path_display"`
	PathIDs     []string `json:"path_ids"`
}

// DashboardMeshHost represents a mesh hostname mapping carried in a route-watch
// snapshot. Either ipv4 or ipv6 (or both) is set; the name is expected to end
// in one of the suffixes the agent advertises in the same snapshot
// (mesh_hosts_suffixes).
type DashboardMeshHost struct {
	Name    string `json:"name"`
	IPv4    string `json:"ipv4,omitempty"`
	IPv6    string `json:"ipv6,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// DashboardResponse mirrors the route-watch snapshot the Muti Metroo agent pushes over
// the SOCKS5 endpoint (CmdRouteWatch / CmdSnapshot).
type DashboardResponse struct {
	Routes            []DashboardRoute    `json:"routes"`
	DomainRoutes      []interface{}       `json:"domain_routes,omitempty"` // Ignored
	MeshHosts         []DashboardMeshHost `json:"mesh_hosts,omitempty"`
	MeshHostsSuffixes []string            `json:"mesh_hosts_suffixes,omitempty"`
}

// PathResult contains mesh path information for a route
type PathResult struct {
	Network     string
	Origin      string
	OriginID    string
	PathDisplay []string
	PathIDs     []string
	HopCount    int
}

// LookupPathFromSnapshot runs a longest-prefix match against a raw dashboard
// snapshot fetched over SOCKS5 via CmdSnapshot. Returns a nil result (no error)
// when no CIDR route contains ip.
func LookupPathFromSnapshot(snapshot []byte, ip net.IP) (*PathResult, error) {
	var dashboard DashboardResponse
	if err := json.Unmarshal(snapshot, &dashboard); err != nil {
		return nil, fmt.Errorf("failed to parse snapshot: %w", err)
	}
	return lookupPathInDashboard(&dashboard, ip), nil
}

// lookupPathInDashboard selects the longest-prefix CIDR route that contains ip.
func lookupPathInDashboard(dashboard *DashboardResponse, ip net.IP) *PathResult {
	var bestMatch *DashboardRoute
	var bestPrefixLen int = -1

	for i := range dashboard.Routes {
		r := &dashboard.Routes[i]
		if r.RouteType != "cidr" {
			continue
		}

		_, ipNet, err := net.ParseCIDR(r.Network)
		if err != nil {
			continue
		}

		if !ipNet.Contains(ip) {
			continue
		}

		ones, _ := ipNet.Mask.Size()
		if ones > bestPrefixLen {
			bestPrefixLen = ones
			bestMatch = r
		}
	}

	if bestMatch == nil {
		return nil // No matching route in mesh
	}

	return &PathResult{
		Network:     bestMatch.Network,
		Origin:      bestMatch.Origin,
		OriginID:    bestMatch.OriginID,
		PathDisplay: bestMatch.PathDisplay,
		PathIDs:     bestMatch.PathIDs,
		HopCount:    bestMatch.HopCount,
	}
}
