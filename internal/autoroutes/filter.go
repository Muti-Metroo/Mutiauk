package autoroutes

import (
	"net"

	"github.com/postalsys/mutiauk/internal/route"
)

// unsafeRoutes are routes that should never be added automatically.
// These could break local networking or capture unintended traffic.
// Users can still add these manually in the config if needed.
var unsafeRoutes = map[string]bool{
	// Default routes - would capture all traffic
	"0.0.0.0/0": true,
	"::/0":      true,

	// Loopback - localhost should never route externally
	"127.0.0.0/8": true,
	"::1/128":     true,

	// Link-local - breaks local network discovery (DHCP, ARP, etc.)
	"169.254.0.0/16": true,
	"fe80::/10":      true,

	// Multicast - not routable through SOCKS proxy
	"224.0.0.0/4": true,
	"ff00::/8":    true,

	// Broadcast
	"255.255.255.255/32": true,
}

// FilterRoutes filters dashboard routes to only include valid CIDR routes
// that should be applied to the local routing table.
// It excludes:
// - Domain-based routes (RouteType == "domain")
// - Unsafe routes (default, loopback, link-local, multicast, broadcast)
// - Invalid CIDR notation
// Returns AutoRoute with origin information for conflict resolution.
func FilterRoutes(routes []DashboardRoute, tunName string) []AutoRoute {
	var result []AutoRoute

	for _, r := range routes {
		// Skip domain routes
		if r.RouteType == "domain" {
			continue
		}

		// Skip unsafe routes (exact match)
		if unsafeRoutes[r.Network] {
			continue
		}

		// Parse CIDR
		_, ipNet, err := net.ParseCIDR(r.Network)
		if err != nil {
			// Invalid CIDR, skip
			continue
		}

		// Also check normalized form (e.g., "127.0.0.1/8" normalizes to "127.0.0.0/8")
		if unsafeRoutes[ipNet.String()] {
			continue
		}

		result = append(result, AutoRoute{
			Route: route.Route{
				Destination: ipNet,
				Interface:   tunName,
				Comment:     "autoroute:" + r.OriginID,
				Enabled:     true,
			},
			OriginID: r.OriginID,
		})
	}

	return result
}
