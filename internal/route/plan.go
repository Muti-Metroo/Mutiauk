package route

import (
	"fmt"
	"net"
)

// Plan represents desired vs actual route state
type Plan struct {
	ToAdd    []Route
	ToRemove []Route
}

// IsEmpty returns true if the plan has no changes
func (p *Plan) IsEmpty() bool {
	return len(p.ToAdd) == 0 && len(p.ToRemove) == 0
}

// String returns a human-readable representation of the plan
func (p *Plan) String() string {
	if p.IsEmpty() {
		return "No changes"
	}

	var s string
	if len(p.ToAdd) > 0 {
		s += fmt.Sprintf("Add %d route(s):\n", len(p.ToAdd))
		for _, r := range p.ToAdd {
			s += fmt.Sprintf("  + %s\n", r.Destination.String())
		}
	}
	if len(p.ToRemove) > 0 {
		s += fmt.Sprintf("Remove %d route(s):\n", len(p.ToRemove))
		for _, r := range p.ToRemove {
			s += fmt.Sprintf("  - %s\n", r.Destination.String())
		}
	}
	return s
}

// Diff compares desired routes with current state
func (m *Manager) Diff(desired []Route) (*Plan, error) {
	current, err := m.ListKernel()
	if err != nil {
		return nil, fmt.Errorf("failed to list current routes: %w", err)
	}

	plan := &Plan{}

	// Build maps for comparison
	currentMap := make(map[string]Route)
	for _, r := range current {
		if r.Destination != nil {
			currentMap[r.Destination.String()] = r
		}
	}

	desiredMap := make(map[string]Route)
	for _, r := range desired {
		if r.Destination != nil && r.Enabled {
			desiredMap[r.Destination.String()] = r
		}
	}

	// Find routes to add
	for key, r := range desiredMap {
		if _, exists := currentMap[key]; !exists {
			plan.ToAdd = append(plan.ToAdd, r)
		}
	}

	// Find routes to remove
	for key, r := range currentMap {
		if _, exists := desiredMap[key]; !exists {
			plan.ToRemove = append(plan.ToRemove, r)
		}
	}

	return plan, nil
}

// Apply applies a plan
func (m *Manager) Apply(plan *Plan) error {
	if plan == nil || plan.IsEmpty() {
		return nil
	}

	var errors []error

	// Remove routes first
	for _, r := range plan.ToRemove {
		if err := m.Remove(r); err != nil {
			errors = append(errors, fmt.Errorf("failed to remove %s: %w", r.Destination.String(), err))
		}
	}

	// Add routes
	for _, r := range plan.ToAdd {
		if err := m.Add(r); err != nil {
			errors = append(errors, fmt.Errorf("failed to add %s: %w", r.Destination.String(), err))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("apply had %d errors: %v", len(errors), errors)
	}

	return nil
}

// routeContains checks if network a contains network b
func routeContains(a, b *net.IPNet) bool {
	if a == nil || b == nil {
		return false
	}

	// a contains b if all of b's addresses are in a
	aOnes, aBits := a.Mask.Size()
	bOnes, bBits := b.Mask.Size()

	if aBits != bBits {
		return false // Different address families
	}

	// a must have fewer or equal bits (larger or equal network)
	if aOnes > bOnes {
		return false
	}

	// Check if b's network address is within a
	return a.Contains(b.IP)
}

// routeOverlaps checks if two networks overlap
func routeOverlaps(a, b *net.IPNet) bool {
	return routeContains(a, b) || routeContains(b, a)
}
