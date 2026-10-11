package route

import (
	"net"
	"sync"

	"go.uber.org/zap"
)

// Route represents a routing entry.
type Route struct {
	Destination *net.IPNet `json:"destination"`
	Gateway     net.IP     `json:"gateway,omitempty"`
	Interface   string     `json:"interface"`
	Metric      int        `json:"metric,omitempty"`
	Comment     string     `json:"comment,omitempty"`
	Enabled     bool       `json:"enabled"`
}

// Manager coordinates route operations between the kernel routing table and persistent state.
type Manager struct {
	mu     sync.RWMutex
	kernel *KernelRoutes
	state  *StateStore
	logger *zap.Logger
}

// DefaultStateFile returns the default persisted-route state-file path
// for the current OS (see paths_{unix,windows}.go).
func DefaultStateFile() string { return defaultRouteStateFile }

// NewManager creates a new route manager for the given TUN interface.
func NewManager(tunName string, logger *zap.Logger) (*Manager, error) {
	return &Manager{
		kernel: NewKernelRoutes(tunName),
		state:  NewStateStore(defaultRouteStateFile),
		logger: logger,
	}, nil
}

// SetStateFile sets the state file path.
func (m *Manager) SetStateFile(path string) {
	m.state.SetPath(path)
}

// Add adds a route to both the kernel and persistent state (idempotent).
func (m *Manager) Add(r Route) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.kernel.RefreshLink(); err != nil {
		return err
	}

	added, err := m.kernel.Add(r)
	if err != nil {
		return err
	}

	if added {
		m.logger.Info("added route",
			zap.String("destination", r.Destination.String()),
			zap.String("interface", m.kernel.TUNName()),
		)
	} else {
		m.logger.Debug("route already exists",
			zap.String("destination", r.Destination.String()),
		)
	}

	r.Interface = m.kernel.TUNName()
	return m.state.AddRoute(r)
}

// Remove removes a route from both the kernel and persistent state (idempotent).
func (m *Manager) Remove(r Route) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Try to refresh link, but proceed even if it fails
	if err := m.kernel.RefreshLink(); err != nil {
		m.logger.Debug("link not found, route considered removed")
		return m.state.RemoveRoute(r)
	}

	removed, err := m.kernel.Remove(r)
	if err != nil {
		return err
	}

	if removed {
		m.logger.Info("removed route",
			zap.String("destination", r.Destination.String()),
		)
	} else {
		m.logger.Debug("route already removed",
			zap.String("destination", r.Destination.String()),
		)
	}

	return m.state.RemoveRoute(r)
}

// List returns routes from persistent state.
func (m *Manager) List() ([]Route, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state, err := m.state.Load()
	if err != nil {
		return nil, err
	}

	routes := make([]Route, 0, len(state))
	for _, r := range state {
		r.Interface = m.kernel.TUNName()
		routes = append(routes, r)
	}

	return routes, nil
}

// ListKernel returns all routes for the TUN interface from the kernel.
func (m *Manager) ListKernel() ([]Route, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if err := m.kernel.RefreshLink(); err != nil {
		return nil, nil // No link, no routes
	}

	return m.kernel.List()
}

// loadState loads route state from file (internal use and conflict detection).
func (m *Manager) loadState() (State, error) {
	return m.state.Load()
}

// Sync synchronizes routes to match desired state.
func (m *Manager) Sync(desired []Route) error {
	plan, err := m.Diff(desired)
	if err != nil {
		return err
	}
	return m.Apply(plan)
}
