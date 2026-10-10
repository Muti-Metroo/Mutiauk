//go:build windows

package route

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// KernelRoutes handles route operations on the Windows routing table via
// the IP Helper "*2" API (winipcfg wraps CreateIpForwardEntry2 etc.).
// Routes are keyed on the TUN adapter's interface LUID, which is stable
// across ifIndex changes -- the robust identity for our adapter.
type KernelRoutes struct {
	mu      sync.RWMutex
	tunName string
	luid    winipcfg.LUID
	ifIndex uint32
	exists  bool
}

// NewKernelRoutes creates a route handler for the given TUN interface.
func NewKernelRoutes(tunName string) *KernelRoutes {
	kr := &KernelRoutes{tunName: tunName}
	kr.RefreshLink() // best-effort; the device may not exist yet
	return kr
}

// RefreshLink re-resolves the cached interface LUID and index.
func (k *KernelRoutes) RefreshLink() error {
	k.mu.Lock()
	defer k.mu.Unlock()

	iface, err := net.InterfaceByName(k.tunName)
	if err != nil {
		k.exists = false
		k.ifIndex = 0
		return fmt.Errorf("failed to get interface %s: %w", k.tunName, err)
	}
	luid, err := winipcfg.LUIDFromIndex(uint32(iface.Index))
	if err != nil {
		k.exists = false
		return fmt.Errorf("failed to resolve LUID for %s: %w", k.tunName, err)
	}
	k.luid = luid
	k.ifIndex = uint32(iface.Index)
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

// Add adds a route to the kernel routing table. Returns true if added,
// false if it already existed (idempotent).
func (k *KernelRoutes) Add(r Route) (added bool, err error) {
	if r.Destination == nil {
		return false, fmt.Errorf("route has no destination")
	}
	if exists, err := k.RouteExists(r.Destination); err == nil && exists {
		return false, nil
	}

	dest, ok := ipNetToPrefix(r.Destination)
	if !ok {
		return false, fmt.Errorf("invalid destination %v", r.Destination)
	}
	luid, nextHop := k.luidAndNextHop(r, dest.Addr().Is6())

	if err := luid.AddRoute(dest, nextHop, uint32(r.Metric)); err != nil {
		if errors.Is(err, windows.ERROR_OBJECT_ALREADY_EXISTS) {
			return false, nil
		}
		return false, fmt.Errorf("add route %s: %w", r.Destination, err)
	}
	return true, nil
}

// Remove removes a route from the kernel routing table. Returns true if
// removed, false if it did not exist (idempotent).
func (k *KernelRoutes) Remove(r Route) (removed bool, err error) {
	if r.Destination == nil {
		return false, fmt.Errorf("route has no destination")
	}

	dest, ok := ipNetToPrefix(r.Destination)
	if !ok {
		return false, fmt.Errorf("invalid destination %v", r.Destination)
	}
	luid, nextHop := k.luidAndNextHop(r, dest.Addr().Is6())

	if err := luid.DeleteRoute(dest, nextHop); err != nil {
		if errors.Is(err, windows.ERROR_NOT_FOUND) {
			return false, nil
		}
		return false, fmt.Errorf("delete route %s: %w", r.Destination, err)
	}
	return true, nil
}

// RouteExists reports whether a route with the given destination exists
// on the TUN interface.
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

// List returns the user-installed routes on the TUN interface. It
// deliberately EXCLUDES the routes Windows auto-creates for the
// interface (its own address/subnet, multicast, broadcast, link-local):
// the declarative Sync removes any listed route not in the desired set,
// and removing those system routes is harmful on Windows. Dropping the
// local /32 makes the OS treat inbound packets to the TUN address as
// "Not locally destined" and silently drop them (breaking the return
// path of every forwarded flow); dropping the on-link subnet breaks
// source-address selection and neighbor resolution out the interface.
func (k *KernelRoutes) List() ([]Route, error) {
	k.mu.RLock()
	luid, exists := k.luid, k.exists
	k.mu.RUnlock()

	if !exists {
		return nil, nil
	}

	table, err := winipcfg.GetIPForwardTable2(winipcfg.AddressFamily(windows.AF_UNSPEC))
	if err != nil {
		return nil, fmt.Errorf("get route table: %w", err)
	}

	ifaceAddrs := k.interfaceAddrs()

	var routes []Route
	for i := range table {
		row := &table[i]
		if row.InterfaceLUID != luid {
			continue
		}
		prefix := row.DestinationPrefix.Prefix()
		if isSystemRoute(prefix, ifaceAddrs) {
			continue
		}
		dst := prefixToIPNet(prefix)
		if dst == nil {
			continue
		}
		r := Route{Destination: dst, Interface: k.tunName, Enabled: true}
		if gw := row.NextHop.Addr(); gw.IsValid() && !gw.IsUnspecified() {
			r.Gateway = gw.AsSlice()
		}
		routes = append(routes, r)
	}
	return routes, nil
}

// interfaceAddrs returns the unicast address prefixes assigned to the TUN
// interface (e.g. 10.200.200.1/24), used to recognize the interface's own
// host and subnet routes.
func (k *KernelRoutes) interfaceAddrs() []netip.Prefix {
	var out []netip.Prefix
	for _, fam := range []winipcfg.AddressFamily{
		winipcfg.AddressFamily(windows.AF_INET),
		winipcfg.AddressFamily(windows.AF_INET6),
	} {
		table, err := winipcfg.GetUnicastIPAddressTable(fam)
		if err != nil {
			continue
		}
		for i := range table {
			row := &table[i]
			if row.InterfaceLUID != k.luid {
				continue
			}
			addr := row.Address.Addr()
			if addr.IsValid() {
				out = append(out, netip.PrefixFrom(addr, int(row.OnLinkPrefixLength)))
			}
		}
	}
	return out
}

// isSystemRoute reports whether prefix is one of the routes Windows
// auto-manages for the interface and that we must never remove: the
// interface's own host route and on-link subnet, plus multicast,
// broadcast, link-local, loopback, and unspecified routes.
func isSystemRoute(prefix netip.Prefix, ifaceAddrs []netip.Prefix) bool {
	addr := prefix.Addr()
	if addr.IsMulticast() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsLoopback() || addr.IsUnspecified() {
		return true
	}
	// IPv4 limited broadcast.
	if addr.Is4() && prefix.Bits() == 32 && addr == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return true
	}
	for _, ia := range ifaceAddrs {
		if prefix.Bits() == ia.Addr().BitLen() && prefix.Addr() == ia.Addr() {
			return true // the interface's own host route (/32 or /128)
		}
		if prefix == ia.Masked() {
			return true // the on-link subnet route
		}
	}
	return false
}

// luidAndNextHop returns the cached interface LUID and the next-hop
// address for a route: the configured gateway, or the unspecified
// address (on-link route through the TUN interface) when none is set.
func (k *KernelRoutes) luidAndNextHop(r Route, v6 bool) (winipcfg.LUID, netip.Addr) {
	k.mu.RLock()
	luid := k.luid
	k.mu.RUnlock()

	nextHop := unspecifiedAddr(v6)
	if r.Gateway != nil {
		if gw, ok := netip.AddrFromSlice(r.Gateway); ok {
			nextHop = gw.Unmap()
		}
	}
	return luid, nextHop
}

// ipNetToPrefix converts a *net.IPNet into a netip.Prefix.
func ipNetToPrefix(n *net.IPNet) (netip.Prefix, bool) {
	addr, ok := netip.AddrFromSlice(n.IP)
	if !ok {
		return netip.Prefix{}, false
	}
	addr = addr.Unmap()
	ones, _ := n.Mask.Size()
	return netip.PrefixFrom(addr, ones), true
}

// prefixToIPNet converts a netip.Prefix into a *net.IPNet.
func prefixToIPNet(p netip.Prefix) *net.IPNet {
	if !p.IsValid() {
		return nil
	}
	bits := 32
	if p.Addr().Is6() {
		bits = 128
	}
	return &net.IPNet{IP: p.Addr().AsSlice(), Mask: net.CIDRMask(p.Bits(), bits)}
}

// unspecifiedAddr returns the family-appropriate unspecified address used
// to express an on-link (interface) route.
func unspecifiedAddr(v6 bool) netip.Addr {
	if v6 {
		return netip.IPv6Unspecified()
	}
	return netip.IPv4Unspecified()
}
