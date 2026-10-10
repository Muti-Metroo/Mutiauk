//go:build linux

package route

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/vishvananda/netlink"
)

// KernelRoutes handles route operations on the kernel routing table via netlink.
type KernelRoutes struct {
	mu      sync.RWMutex
	tunName string
	link    netlink.Link
}

// NewKernelRoutes creates a new kernel routes handler for the given TUN interface.
func NewKernelRoutes(tunName string) *KernelRoutes {
	kr := &KernelRoutes{tunName: tunName}

	// Try to get the link (may not exist yet if daemon not running)
	if link, err := netlink.LinkByName(tunName); err == nil {
		kr.link = link
	}

	return kr
}

// RefreshLink refreshes the netlink reference to the TUN interface.
func (k *KernelRoutes) RefreshLink() error {
	k.mu.Lock()
	defer k.mu.Unlock()

	link, err := netlink.LinkByName(k.tunName)
	if err != nil {
		return fmt.Errorf("failed to get link %s: %w", k.tunName, err)
	}
	k.link = link
	return nil
}

// LinkExists returns true if the TUN interface currently exists.
func (k *KernelRoutes) LinkExists() bool {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.link != nil
}

// TUNName returns the TUN interface name.
func (k *KernelRoutes) TUNName() string {
	return k.tunName
}

// routeExistsLocked checks if a route exists (caller must hold lock).
func (k *KernelRoutes) routeExistsLocked(destination *net.IPNet) (bool, error) {
	if k.link == nil {
		return false, errors.New("link not available")
	}

	routes, err := netlink.RouteList(k.link, netlink.FAMILY_ALL)
	if err != nil {
		return false, fmt.Errorf("failed to list routes: %w", err)
	}

	for _, existing := range routes {
		if existing.Dst != nil && existing.Dst.String() == destination.String() {
			return true, nil
		}
	}
	return false, nil
}

// RouteExists checks if a route with the given destination exists on the TUN interface.
func (k *KernelRoutes) RouteExists(destination *net.IPNet) (bool, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.routeExistsLocked(destination)
}

// Add adds a route to the kernel routing table. Returns true if the route was added,
// false if it already existed (idempotent).
func (k *KernelRoutes) Add(r Route) (added bool, err error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	exists, err := k.routeExistsLocked(r.Destination)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}

	// Build netlink route
	nlRoute := &netlink.Route{
		LinkIndex: k.link.Attrs().Index,
		Dst:       r.Destination,
		Scope:     netlink.SCOPE_LINK,
	}

	if r.Gateway != nil {
		nlRoute.Gw = r.Gateway
		nlRoute.Scope = netlink.SCOPE_UNIVERSE
	}

	if r.Metric > 0 {
		nlRoute.Priority = r.Metric
	}

	if err := netlink.RouteAdd(nlRoute); err != nil {
		return false, fmt.Errorf("failed to add route: %w", err)
	}

	return true, nil
}

// Remove removes a route from the kernel routing table. Returns true if the route was
// removed, false if it did not exist (idempotent).
func (k *KernelRoutes) Remove(r Route) (removed bool, err error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.link == nil {
		return false, nil // No link means no route
	}

	nlRoute := &netlink.Route{
		LinkIndex: k.link.Attrs().Index,
		Dst:       r.Destination,
	}

	if err := netlink.RouteDel(nlRoute); err != nil {
		// Route doesn't exist - not an error for idempotent operation
		if os.IsNotExist(err) || err.Error() == "no such process" {
			return false, nil
		}
		return false, fmt.Errorf("failed to remove route: %w", err)
	}

	return true, nil
}

// List returns all routes on the TUN interface from the kernel.
func (k *KernelRoutes) List() ([]Route, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()

	if k.link == nil {
		return nil, nil // No link, no routes
	}

	nlRoutes, err := netlink.RouteList(k.link, netlink.FAMILY_ALL)
	if err != nil {
		return nil, fmt.Errorf("failed to list routes: %w", err)
	}

	var routes []Route
	for _, nlr := range nlRoutes {
		if nlr.Dst == nil {
			continue
		}
		routes = append(routes, Route{
			Destination: nlr.Dst,
			Gateway:     nlr.Gw,
			Interface:   k.tunName,
			Metric:      nlr.Priority,
			Enabled:     true,
		})
	}

	return routes, nil
}
