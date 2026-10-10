package autoroutes

import (
	"net"
	"sort"
	"strings"

	"github.com/postalsys/mutiauk/internal/route"
)

// AutoRoute is a route with origin information for conflict resolution
type AutoRoute struct {
	Route    route.Route
	OriginID string
}

// ResolveConflicts deduplicates routes and resolves conflicts.
// For duplicate networks from different agents, keeps the one with
// lexicographically smallest OriginID for deterministic behavior.
func ResolveConflicts(routes []AutoRoute) []route.Route {
	if len(routes) == 0 {
		return nil
	}

	// Group by network string
	byNetwork := make(map[string]AutoRoute)
	for _, r := range routes {
		key := r.Route.Destination.String()
		existing, found := byNetwork[key]
		if !found {
			byNetwork[key] = r
			continue
		}

		// Keep the one with smaller OriginID (deterministic choice)
		if r.OriginID < existing.OriginID {
			byNetwork[key] = r
		}
	}

	// Convert back to slice
	result := make([]route.Route, 0, len(byNetwork))
	for _, ar := range byNetwork {
		result = append(result, ar.Route)
	}

	// Sort by destination for consistent ordering
	sort.Slice(result, func(i, j int) bool {
		return result[i].Destination.String() < result[j].Destination.String()
	})

	return result
}

// MergeRoutes merges config routes with autoroutes.
// Config routes take precedence - any autoroute with the same
// network as a config route is dropped.
func MergeRoutes(configRoutes, autoRoutes []route.Route) []route.Route {
	// Build set of config CIDR strings
	configSet := make(map[string]bool)
	for _, r := range configRoutes {
		if r.Destination != nil {
			configSet[r.Destination.String()] = true
		}
	}

	// Start with all config routes
	result := make([]route.Route, len(configRoutes))
	copy(result, configRoutes)

	// Add autoroutes that don't conflict with config
	for _, r := range autoRoutes {
		if r.Destination == nil {
			continue
		}
		if !configSet[r.Destination.String()] {
			result = append(result, r)
		}
	}

	return result
}

// extractOriginID extracts the origin ID from the route comment
// Comment format: "autoroute:<origin_id>"
func extractOriginID(r route.Route) string {
	if strings.HasPrefix(r.Comment, "autoroute:") {
		return strings.TrimPrefix(r.Comment, "autoroute:")
	}
	return ""
}

// routeContains checks if network a completely contains network b
func routeContains(a, b *net.IPNet) bool {
	aOnes, aBits := a.Mask.Size()
	bOnes, bBits := b.Mask.Size()

	// Must be same IP family
	if aBits != bBits {
		return false
	}

	// a must be less specific (smaller prefix) to contain b
	if aOnes > bOnes {
		return false
	}

	// b's network must be within a
	return a.Contains(b.IP)
}
