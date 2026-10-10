//go:build windows

package route

import (
	"net"
	"net/netip"
	"testing"
)

func TestIPNetPrefixRoundTrip(t *testing.T) {
	tests := []string{
		"10.0.0.0/8",
		"192.168.1.0/24",
		"10.200.200.1/32",
		"fd00::/64",
		"2001:db8::1/128",
	}
	for _, cidr := range tests {
		t.Run(cidr, func(t *testing.T) {
			_, ipNet, err := net.ParseCIDR(cidr)
			if err != nil {
				t.Fatal(err)
			}
			p, ok := ipNetToPrefix(ipNet)
			if !ok {
				t.Fatalf("ipNetToPrefix(%s) not ok", cidr)
			}
			back := prefixToIPNet(p)
			if back == nil {
				t.Fatalf("prefixToIPNet returned nil for %s", cidr)
			}
			if back.String() != ipNet.String() {
				t.Errorf("round trip = %s, want %s", back.String(), ipNet.String())
			}
		})
	}
}

func TestUnspecifiedAddr(t *testing.T) {
	if got := unspecifiedAddr(false); got.String() != "0.0.0.0" {
		t.Errorf("unspecifiedAddr(v4) = %s, want 0.0.0.0", got)
	}
	if got := unspecifiedAddr(true); got.String() != "::" {
		t.Errorf("unspecifiedAddr(v6) = %s, want ::", got)
	}
}

// TestIsSystemRoute guards the route-preservation fix: Sync must never
// remove Windows' auto-created interface routes (the local /32, the
// on-link subnet, multicast/broadcast/link-local), only user routes.
// Removing the local /32 makes the OS drop inbound TUN packets as "Not
// locally destined", breaking the return path of every forwarded flow.
func TestIsSystemRoute(t *testing.T) {
	ifaceAddrs := []netip.Prefix{
		netip.MustParsePrefix("10.200.200.1/24"),
		netip.MustParsePrefix("fd00:200::1/64"),
	}
	tests := []struct {
		prefix string
		system bool
	}{
		{"10.200.200.1/32", true},    // interface's own host route
		{"10.200.200.0/24", true},    // on-link subnet
		{"fd00:200::1/128", true},    // ipv6 host route
		{"fd00:200::/64", true},      // ipv6 on-link subnet
		{"224.0.0.0/4", true},        // multicast
		{"239.255.255.250/32", true}, // multicast (SSDP)
		{"255.255.255.255/32", true}, // limited broadcast
		{"169.254.0.0/16", true},     // link-local v4
		{"fe80::/64", true},          // link-local v6
		{"ff00::/8", true},           // multicast v6
		{"1.1.1.1/32", false},        // user route (must be managed)
		{"10.123.0.0/16", false},     // user route
		{"0.0.0.0/0", true},          // default via unspecified addr -> treated as system
	}
	for _, tt := range tests {
		p := netip.MustParsePrefix(tt.prefix)
		if got := isSystemRoute(p, ifaceAddrs); got != tt.system {
			t.Errorf("isSystemRoute(%s) = %v, want %v", tt.prefix, got, tt.system)
		}
	}
}
