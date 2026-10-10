package route

import (
	"fmt"
)

// Conflict represents a routing conflict
type Conflict struct {
	Existing Route
	Proposed Route
	Reason   string
}

// String returns a human-readable conflict description
func (c *Conflict) String() string {
	return fmt.Sprintf("%s conflicts with %s: %s",
		c.Proposed.Destination.String(),
		c.Existing.Destination.String(),
		c.Reason,
	)
}

// DetectConflicts checks for routing conflicts
func (m *Manager) DetectConflicts(routes []Route) ([]Conflict, error) {
	var conflicts []Conflict

	// Check for duplicates and overlaps within the proposed routes
	for i, r1 := range routes {
		for j, r2 := range routes {
			if i >= j {
				continue
			}

			if r1.Destination == nil || r2.Destination == nil {
				continue
			}

			// Check for exact duplicates
			if r1.Destination.String() == r2.Destination.String() {
				conflicts = append(conflicts, Conflict{
					Existing: r1,
					Proposed: r2,
					Reason:   "duplicate route",
				})
				continue
			}

			// Check for overlapping networks
			if routeOverlaps(r1.Destination, r2.Destination) {
				var reason string
				if routeContains(r1.Destination, r2.Destination) {
					reason = fmt.Sprintf("%s is contained within %s",
						r2.Destination.String(), r1.Destination.String())
				} else {
					reason = fmt.Sprintf("%s is contained within %s",
						r1.Destination.String(), r2.Destination.String())
				}
				conflicts = append(conflicts, Conflict{
					Existing: r1,
					Proposed: r2,
					Reason:   reason,
				})
			}
		}
	}

	// Check against existing kernel routes (not managed by us)
	kernelRoutes, err := m.ListKernel()
	if err != nil {
		// Can't check kernel routes, just return what we have
		return conflicts, nil
	}

	managedState, _ := m.loadState()
	if managedState == nil {
		managedState = make(State)
	}

	for _, proposed := range routes {
		if proposed.Destination == nil {
			continue
		}

		for _, existing := range kernelRoutes {
			if existing.Destination == nil {
				continue
			}

			// Skip if this is one of our managed routes
			if _, isManaged := managedState[existing.Destination.String()]; isManaged {
				continue
			}

			// Check for exact match (conflict with system route)
			if proposed.Destination.String() == existing.Destination.String() {
				conflicts = append(conflicts, Conflict{
					Existing: existing,
					Proposed: proposed,
					Reason:   "conflicts with existing system route",
				})
				continue
			}

			// Check if proposed route would shadow an existing route
			if routeContains(proposed.Destination, existing.Destination) {
				conflicts = append(conflicts, Conflict{
					Existing: existing,
					Proposed: proposed,
					Reason:   "would shadow existing route",
				})
			}
		}
	}

	return conflicts, nil
}

// ValidateRoutes validates a list of routes
func ValidateRoutes(routes []Route) []error {
	var errors []error

	for i, r := range routes {
		if r.Destination == nil {
			errors = append(errors, fmt.Errorf("routes[%d]: destination is required", i))
			continue
		}

		// Check for valid CIDR
		ones, bits := r.Destination.Mask.Size()
		if bits == 0 {
			errors = append(errors, fmt.Errorf("routes[%d]: invalid network mask", i))
		}

		// Warn about very broad routes
		if ones < 8 && bits == 32 {
			errors = append(errors, fmt.Errorf("routes[%d]: very broad IPv4 route (/%d)", i, ones))
		}
		if ones < 16 && bits == 128 {
			errors = append(errors, fmt.Errorf("routes[%d]: very broad IPv6 route (/%d)", i, ones))
		}

		// Check gateway if specified
		if r.Gateway != nil {
			if r.Gateway.To4() != nil && r.Destination.IP.To4() == nil {
				errors = append(errors, fmt.Errorf("routes[%d]: IPv4 gateway with IPv6 destination", i))
			}
			if r.Gateway.To4() == nil && r.Destination.IP.To4() != nil {
				errors = append(errors, fmt.Errorf("routes[%d]: IPv6 gateway with IPv4 destination", i))
			}
		}
	}

	return errors
}
