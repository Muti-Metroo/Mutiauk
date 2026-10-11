//go:build darwin

package route

import (
	"fmt"
	"net"
	"strings"
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

// Lookup finds the kernel route for a destination IP using `route -n get`.
func Lookup(ip net.IP, tunName string) (*LookupResult, error) {
	args := []string{"-n", "get"}
	if ip.To4() == nil {
		args = append(args, "-inet6")
	}
	args = append(args, ip.String())

	out, err := runRoute(args...)
	if err != nil {
		return nil, fmt.Errorf("route get %s: %w (%s)", ip, err, strings.TrimSpace(out))
	}

	fields := parseRouteGet(out)
	result := &LookupResult{
		ResolvedIP:   ip,
		Interface:    fields["interface"],
		MatchedRoute: matchedRoute(fields["destination"], fields["mask"], ip),
	}
	result.IsMutiauk = result.Interface != "" && result.Interface == tunName
	if gw := net.ParseIP(fields["gateway"]); gw != nil {
		result.Gateway = gw
	}
	return result, nil
}

// ResolveAndLookup resolves a domain/IP and looks up the route.
func ResolveAndLookup(destination string, tunName string) (*LookupResult, error) {
	ip := net.ParseIP(destination)
	if ip == nil {
		ips, err := net.LookupIP(destination)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve %s: %w", destination, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no IP addresses for %s", destination)
		}
		ip = ips[0]
		for _, addr := range ips {
			if addr.To4() != nil {
				ip = addr // prefer IPv4
				break
			}
		}
	}

	result, err := Lookup(ip, tunName)
	if err != nil {
		return nil, err
	}
	result.Destination = destination
	return result, nil
}

// parseRouteGet parses the "key: value" lines emitted by `route get`.
func parseRouteGet(out string) map[string]string {
	fields := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return fields
}

// matchedRoute reconstructs the matched CIDR from the textual destination and
// mask reported by `route get`. The default route and unparseable masks are
// handled gracefully (best-effort, as this drives diagnostics only).
func matchedRoute(dst, mask string, ip net.IP) *net.IPNet {
	v4 := ip.To4() != nil
	bits := 32
	zero := net.IPv4zero
	if !v4 {
		bits = 128
		zero = net.IPv6zero
	}

	if dst == "" || dst == "default" {
		return &net.IPNet{IP: zero, Mask: net.CIDRMask(0, bits)}
	}
	dstIP := net.ParseIP(dst)
	if dstIP == nil {
		return nil
	}

	ones := bits // default to a host route
	if mask != "" && mask != "default" {
		if v4 {
			if m := net.ParseIP(mask); m != nil && m.To4() != nil {
				if o, _ := net.IPMask(m.To4()).Size(); o > 0 {
					ones = o
				}
			}
		} else if o := hexMaskOnes(mask); o >= 0 {
			ones = o
		}
	}
	return &net.IPNet{IP: dstIP, Mask: net.CIDRMask(ones, bits)}
}

// hexMaskOnes parses an IPv6 netmask in colon-hex form (e.g. "ffff:ff00::")
// into a prefix length, or -1 if it cannot be parsed.
func hexMaskOnes(mask string) int {
	ip := net.ParseIP(mask)
	if ip == nil {
		return -1
	}
	ones, _ := net.IPMask(ip.To16()).Size()
	return ones
}
