//go:build !linux && !darwin && !windows

package route

import (
	"errors"
	"net"
	"sync"
)

var errNotSupported = errors.New("kernel route operations not supported on this platform")

// KernelRoutes handles route operations on the kernel routing table.
// This is a stub implementation for non-Linux platforms.
type KernelRoutes struct {
	mu      sync.RWMutex
	tunName string
}

// NewKernelRoutes creates a new kernel routes handler for the given TUN interface.
func NewKernelRoutes(tunName string) *KernelRoutes {
	return &KernelRoutes{tunName: tunName}
}

// RefreshLink is a no-op on non-Linux platforms.
func (k *KernelRoutes) RefreshLink() error {
	return errNotSupported
}

// LinkExists always returns false on non-Linux platforms.
func (k *KernelRoutes) LinkExists() bool {
	return false
}

// TUNName returns the TUN interface name.
func (k *KernelRoutes) TUNName() string {
	return k.tunName
}

// routeExistsLocked is a no-op on non-Linux platforms.
func (k *KernelRoutes) routeExistsLocked(destination *net.IPNet) (bool, error) {
	return false, errNotSupported
}

// RouteExists always returns false on non-Linux platforms.
func (k *KernelRoutes) RouteExists(destination *net.IPNet) (bool, error) {
	return false, errNotSupported
}

// Add is a no-op on non-Linux platforms.
func (k *KernelRoutes) Add(r Route) (added bool, err error) {
	return k.add(r, true)
}

// add is a no-op on non-Linux platforms.
func (k *KernelRoutes) add(r Route, checkExists bool) (added bool, err error) {
	return false, errNotSupported
}

// Remove is a no-op on non-Linux platforms.
func (k *KernelRoutes) Remove(r Route) (removed bool, err error) {
	return false, errNotSupported
}

// List returns nil on non-Linux platforms.
func (k *KernelRoutes) List() ([]Route, error) {
	return nil, errNotSupported
}
