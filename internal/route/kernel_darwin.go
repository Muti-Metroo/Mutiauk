//go:build darwin

package route

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"

	xroute "golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

// KernelRoutes handles route operations on the macOS routing table. Mutations
// go through the /sbin/route command (the kernel has no netlink-style API and
// PF_ROUTE message marshaling is fiddly); reads use a PF_ROUTE RIB dump parsed
// with golang.org/x/net/route, which avoids netstat's abbreviated output.
type KernelRoutes struct {
	mu      sync.RWMutex
	tunName string
	ifIndex int
	exists  bool
}

// NewKernelRoutes creates a new kernel routes handler for the given TUN interface.
func NewKernelRoutes(tunName string) *KernelRoutes {
	kr := &KernelRoutes{tunName: tunName}
	if iface, err := net.InterfaceByName(tunName); err == nil {
		kr.ifIndex = iface.Index
		kr.exists = true
	}
	return kr
}

// RefreshLink refreshes the cached interface index.
func (k *KernelRoutes) RefreshLink() error {
	k.mu.Lock()
	defer k.mu.Unlock()

	iface, err := net.InterfaceByName(k.tunName)
	if err != nil {
		k.exists = false
		k.ifIndex = 0
		return fmt.Errorf("failed to get interface %s: %w", k.tunName, err)
	}
	k.ifIndex = iface.Index
	k.exists = true
	return nil
}

// LinkExists returns true if the TUN interface currently exists.
func (k *KernelRoutes) LinkExists() bool {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.exists
}

// TUNName returns the TUN interface name.
func (k *KernelRoutes) TUNName() string {
	return k.tunName
}

// Add adds a route to the kernel routing table. Returns true if the route was
// added, false if it already existed (idempotent).
func (k *KernelRoutes) Add(r Route) (added bool, err error) {
	return k.add(r, true)
}

// add adds a route to the kernel routing table. When checkExists is false the
// pre-add existence lookup (a full RIB dump) is skipped: the caller's diff
// already established the route is absent, and `route add` reporting
// "File exists" is treated as "already existed".
func (k *KernelRoutes) add(r Route, checkExists bool) (added bool, err error) {
	if r.Destination == nil {
		return false, fmt.Errorf("route has no destination")
	}

	// macOS `route add` does not reliably fail on duplicates, so check first
	// to keep the operation idempotent.
	if checkExists {
		if exists, err := k.RouteExists(r.Destination); err == nil && exists {
			return false, nil
		}
	}

	args := []string{"-n", "add"}
	if isIPv6Net(r.Destination) {
		args = append(args, "-inet6")
	}
	args = append(args, "-net", r.Destination.String())
	if r.Gateway != nil {
		args = append(args, r.Gateway.String())
	} else {
		args = append(args, "-interface", k.tunName)
	}

	out, err := runRoute(args...)
	if err != nil {
		if strings.Contains(out, "File exists") {
			return false, nil // already present
		}
		return false, fmt.Errorf("route add %s: %w (%s)", r.Destination, err, out)
	}
	return true, nil
}

// Remove removes a route from the kernel routing table. Returns true if the
// route was removed, false if it did not exist (idempotent).
func (k *KernelRoutes) Remove(r Route) (removed bool, err error) {
	if r.Destination == nil {
		return false, fmt.Errorf("route has no destination")
	}

	args := []string{"-n", "delete"}
	if isIPv6Net(r.Destination) {
		args = append(args, "-inet6")
	}
	args = append(args, "-net", r.Destination.String(), "-interface", k.tunName)

	out, err := runRoute(args...)
	if err != nil {
		if strings.Contains(out, "not in table") {
			return false, nil // already gone
		}
		return false, fmt.Errorf("route delete %s: %w (%s)", r.Destination, err, out)
	}
	return true, nil
}

// RouteExists checks if a route with the given destination exists on the TUN interface.
func (k *KernelRoutes) RouteExists(destination *net.IPNet) (bool, error) {
	routes, err := k.List()
	if err != nil {
		return false, err
	}
	for _, r := range routes {
		if r.Destination != nil && r.Destination.String() == destination.String() {
			return true, nil
		}
	}
	return false, nil
}

// List returns all routes on the TUN interface from the kernel.
func (k *KernelRoutes) List() ([]Route, error) {
	k.mu.RLock()
	idx, exists := k.ifIndex, k.exists
	k.mu.RUnlock()

	if !exists {
		return nil, nil
	}

	rib, err := xroute.FetchRIB(unix.AF_UNSPEC, xroute.RIBTypeRoute, 0)
	if err != nil {
		return nil, fmt.Errorf("fetch routing table: %w", err)
	}
	msgs, err := xroute.ParseRIB(xroute.RIBTypeRoute, rib)
	if err != nil {
		return nil, fmt.Errorf("parse routing table: %w", err)
	}

	var routes []Route
	for _, m := range msgs {
		rm, ok := m.(*xroute.RouteMessage)
		if !ok || rm.Index != idx {
			continue
		}
		dst := routeDestination(rm.Addrs)
		if dst == nil {
			continue
		}
		routes = append(routes, Route{
			Destination: dst,
			Gateway:     routeAddrIP(rm.Addrs, unix.RTAX_GATEWAY),
			Interface:   k.tunName,
			Enabled:     true,
		})
	}
	return routes, nil
}

// routeDestination builds the destination CIDR from a route message's DST and
// NETMASK address slots. A missing netmask means a host route.
func routeDestination(addrs []xroute.Addr) *net.IPNet {
	ip := routeAddrIP(addrs, unix.RTAX_DST)
	if ip == nil {
		return nil
	}
	bits := 32
	if ip.To4() == nil {
		bits = 128
	}
	ones := bits
	if mask := maskFromAddr(addrs, bits); mask >= 0 {
		ones = mask
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(ones, bits)}
}

// routeAddrIP extracts the IP at the given RTAX index, or nil if absent or not
// an IP address (e.g. a link-layer gateway for an interface route).
func routeAddrIP(addrs []xroute.Addr, idx int) net.IP {
	if idx >= len(addrs) {
		return nil
	}
	switch a := addrs[idx].(type) {
	case *xroute.Inet4Addr:
		ip := make(net.IP, 4)
		copy(ip, a.IP[:])
		return ip
	case *xroute.Inet6Addr:
		ip := make(net.IP, 16)
		copy(ip, a.IP[:])
		return ip
	}
	return nil
}

// maskFromAddr returns the prefix length from the NETMASK slot, or -1 when
// absent (host route).
func maskFromAddr(addrs []xroute.Addr, bits int) int {
	if unix.RTAX_NETMASK >= len(addrs) {
		return -1
	}
	switch a := addrs[unix.RTAX_NETMASK].(type) {
	case *xroute.Inet4Addr:
		ones, _ := net.IPMask(a.IP[:]).Size()
		return ones
	case *xroute.Inet6Addr:
		ones, _ := net.IPMask(a.IP[:]).Size()
		return ones
	}
	return -1
}

// isIPv6Net reports whether the network is IPv6.
func isIPv6Net(n *net.IPNet) bool {
	return n.IP.To4() == nil
}

// runRoute executes /sbin/route and returns its combined output.
func runRoute(args ...string) (string, error) {
	out, err := exec.Command("/sbin/route", args...).CombinedOutput()
	return string(out), err
}
