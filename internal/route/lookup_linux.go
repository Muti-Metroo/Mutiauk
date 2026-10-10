//go:build linux

package route

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// LookupResult contains route lookup information
type LookupResult struct {
	Destination  string     // Original input (IP or domain)
	ResolvedIP   net.IP     // Resolved IP address
	MatchedRoute *net.IPNet // Best matching route CIDR
	Interface    string     // Interface name
	Gateway      net.IP     // Gateway if any
	IsMutiauk    bool       // True if matches TUN interface
}

// Lookup finds the kernel route for a destination IP
func Lookup(ip net.IP, tunName string) (*LookupResult, error) {
	routes, err := netlink.RouteGet(ip)
	if err != nil {
		return nil, fmt.Errorf("route lookup failed: %w", err)
	}

	if len(routes) == 0 {
		return nil, fmt.Errorf("no route to %s", ip)
	}

	r := routes[0]
	result := &LookupResult{
		ResolvedIP: ip,
		Gateway:    r.Gw,
	}

	// Get interface name
	if r.LinkIndex > 0 {
		link, err := netlink.LinkByIndex(r.LinkIndex)
		if err == nil {
			result.Interface = link.Attrs().Name
			result.IsMutiauk = (result.Interface == tunName)
		}
	}

	// Get matched route CIDR
	if r.Dst != nil {
		result.MatchedRoute = r.Dst
	} else {
		// Default route - determine IP family
		if ip.To4() != nil {
			result.MatchedRoute = &net.IPNet{
				IP:   net.IPv4zero,
				Mask: net.CIDRMask(0, 32),
			}
		} else {
			result.MatchedRoute = &net.IPNet{
				IP:   net.IPv6zero,
				Mask: net.CIDRMask(0, 128),
			}
		}
	}

	return result, nil
}

// ResolveAndLookup resolves a domain/IP and looks up the route
func ResolveAndLookup(destination string, tunName string) (*LookupResult, error) {
	// Try parsing as IP first
	ip := net.ParseIP(destination)
	if ip == nil {
		// Resolve as domain
		ips, err := net.LookupIP(destination)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve %s: %w", destination, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no IP addresses for %s", destination)
		}
		// Prefer IPv4 if available
		for _, addr := range ips {
			if addr.To4() != nil {
				ip = addr
				break
			}
		}
		if ip == nil {
			ip = ips[0]
		}
	}

	result, err := Lookup(ip, tunName)
	if err != nil {
		return nil, err
	}
	result.Destination = destination
	return result, nil
}
