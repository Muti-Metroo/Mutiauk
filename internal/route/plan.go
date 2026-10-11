package route

import (
	"fmt"
	"net"

	"go.uber.org/zap"
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

// ApplyResult reports the per-route outcome of an Apply. Added and Removed
// hold the routes the kernel actually installed or withdrew (routes that were
// already in the desired state are not changes); Failed holds the routes that
// could not be applied, with the cause.
type ApplyResult struct {
	Added   []Route
	Removed []Route
	Failed  []ApplyFailure
}

// ApplyFailure is a single route that could not be applied.
type ApplyFailure struct {
	Route   Route
	Removal bool // true when the failed operation was a removal
	Err     error
}

// Apply applies a plan. Work is batched: one link refresh and one state-file
// read + write for the whole plan, and kernel adds skip the per-route
// existence lookup - the diff already established what the kernel holds, and
// the kernel operations themselves remain idempotent. Per-route behavior
// (removals first, continue past failures, aggregate error) is unchanged.
func (m *Manager) Apply(plan *Plan) (*ApplyResult, error) {
	res := &ApplyResult{}
	if plan == nil || plan.IsEmpty() {
		return res, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// One link refresh for the whole batch instead of one per route.
	linkErr := m.kernel.RefreshLink()

	// Load persisted state once; it is written back once at the end.
	state, stateErr := m.state.Load()
	if stateErr != nil {
		state = nil
	}
	dirty := false

	// Remove routes first
	for _, r := range plan.ToRemove {
		if linkErr != nil {
			// No link: the kernel route is gone with the interface, so the
			// removal reduces to a state update (same as Remove).
			m.logger.Debug("link not found, route considered removed")
			res.Removed = append(res.Removed, r)
			dirty = true
			delete(state, r.Destination.String())
			continue
		}
		removed, err := m.kernel.Remove(r)
		if err != nil {
			res.Failed = append(res.Failed, ApplyFailure{Route: r, Removal: true, Err: err})
			continue
		}
		if removed {
			m.logger.Info("removed route",
				zap.String("destination", r.Destination.String()),
			)
			res.Removed = append(res.Removed, r)
		}
		dirty = true
		delete(state, r.Destination.String())
	}

	// Add routes
	for _, r := range plan.ToAdd {
		if linkErr != nil {
			res.Failed = append(res.Failed, ApplyFailure{Route: r, Err: linkErr})
			continue
		}
		added, err := m.kernel.add(r, false)
		if err != nil {
			res.Failed = append(res.Failed, ApplyFailure{Route: r, Err: err})
			continue
		}
		if added {
			m.logger.Info("added route",
				zap.String("destination", r.Destination.String()),
				zap.String("interface", m.kernel.TUNName()),
			)
			res.Added = append(res.Added, r)
		} else {
			m.logger.Debug("route already exists",
				zap.String("destination", r.Destination.String()),
			)
		}
		dirty = true
		r.Interface = m.kernel.TUNName()
		r.Enabled = true
		if state != nil {
			state[r.Destination.String()] = r
		}
	}

	if stateErr != nil {
		return res, fmt.Errorf("apply could not persist route state: %w", stateErr)
	}
	if dirty {
		if err := m.state.Save(state); err != nil {
			return res, fmt.Errorf("apply could not persist route state: %w", err)
		}
	}

	if len(res.Failed) > 0 {
		errs := make([]error, 0, len(res.Failed))
		for _, f := range res.Failed {
			errs = append(errs, fmt.Errorf("%s: %w", f.Route.Destination.String(), f.Err))
		}
		return res, fmt.Errorf("apply had %d errors: %v", len(errs), errs)
	}

	return res, nil
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
