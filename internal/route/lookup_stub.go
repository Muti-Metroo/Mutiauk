//go:build !linux && !darwin && !windows

package route

import (
	"net"
)

// LookupResult contains route lookup information.
type LookupResult struct {
	Destination  string     // Original input (IP or domain)
	ResolvedIP   net.IP     // Resolved IP address
	MatchedRoute *net.IPNet // Best matching route CIDR
	Interface    string     // Interface name
	Gateway      net.IP     // Gateway if any
	IsMutiauk    bool       // True if matches TUN interface
}

// Lookup returns an error on non-Linux platforms.
func Lookup(ip net.IP, tunName string) (*LookupResult, error) {
	return nil, errNotSupported
}

// ResolveAndLookup returns an error on non-Linux platforms.
func ResolveAndLookup(destination string, tunName string) (*LookupResult, error) {
	return nil, errNotSupported
}
