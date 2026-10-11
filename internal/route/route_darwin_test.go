//go:build darwin

package route

import (
	"net"
	"os"
	"testing"

	"github.com/Muti-Metroo/Mutiauk/internal/tun"
	xroute "golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

func TestIsIPv6Net(t *testing.T) {
	_, v4, _ := net.ParseCIDR("10.0.0.0/24")
	_, v6, _ := net.ParseCIDR("fd00::/64")
	if isIPv6Net(v4) {
		t.Error("10.0.0.0/24 should not be IPv6")
	}
	if !isIPv6Net(v6) {
		t.Error("fd00::/64 should be IPv6")
	}
}

func TestRouteDestination(t *testing.T) {
	addrs := make([]xroute.Addr, 3)
	addrs[unix.RTAX_DST] = &xroute.Inet4Addr{IP: [4]byte{10, 1, 2, 0}}
	addrs[unix.RTAX_NETMASK] = &xroute.Inet4Addr{IP: [4]byte{255, 255, 255, 0}}

	got := routeDestination(addrs)
	if got == nil {
		t.Fatal("routeDestination returned nil")
	}
	if got.String() != "10.1.2.0/24" {
		t.Errorf("routeDestination = %s, want 10.1.2.0/24", got)
	}
}

func TestRouteDestination_HostRoute(t *testing.T) {
	// No netmask slot => host route (/32).
	addrs := make([]xroute.Addr, 1)
	addrs[unix.RTAX_DST] = &xroute.Inet4Addr{IP: [4]byte{10, 1, 2, 3}}

	got := routeDestination(addrs)
	if got == nil || got.String() != "10.1.2.3/32" {
		t.Errorf("routeDestination = %v, want 10.1.2.3/32", got)
	}
}

func TestParseRouteGet(t *testing.T) {
	out := `   route to: 10.99.99.99
destination: 10.99.99.0
       mask: 255.255.255.0
  interface: utun7
      flags: <UP,DONE,STATIC>
`
	fields := parseRouteGet(out)
	if fields["destination"] != "10.99.99.0" {
		t.Errorf("destination = %q, want 10.99.99.0", fields["destination"])
	}
	if fields["mask"] != "255.255.255.0" {
		t.Errorf("mask = %q, want 255.255.255.0", fields["mask"])
	}
	if fields["interface"] != "utun7" {
		t.Errorf("interface = %q, want utun7", fields["interface"])
	}
}

func TestMatchedRoute(t *testing.T) {
	v4 := net.ParseIP("10.99.99.99")
	v6 := net.ParseIP("fd00::1")
	tests := []struct {
		name string
		dst  string
		mask string
		ip   net.IP
		want string
	}{
		{"v4 cidr", "10.99.99.0", "255.255.255.0", v4, "10.99.99.0/24"},
		{"v4 default", "default", "default", v4, "0.0.0.0/0"},
		{"v4 host (no mask)", "10.99.99.5", "", v4, "10.99.99.5/32"},
		{"v6 default", "default", "default", v6, "::/0"},
		{"v6 prefix", "fd00::", "ffff:ffff:ffff:ffff::", v6, "fd00::/64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchedRoute(tt.dst, tt.mask, tt.ip)
			if got == nil {
				t.Fatalf("matchedRoute(%q,%q) = nil", tt.dst, tt.mask)
			}
			if got.String() != tt.want {
				t.Errorf("matchedRoute(%q,%q) = %s, want %s", tt.dst, tt.mask, got, tt.want)
			}
		})
	}
}

// TestKernelRoutes_Integration drives Add/RouteExists/List/Remove/Lookup against
// a real utun interface. It needs root and is skipped otherwise.
func TestKernelRoutes_Integration(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to create a utun and modify the routing table")
	}

	dev, err := tun.New(tun.Config{
		MTU:     1400,
		Address: net.ParseIP("10.200.202.1"),
		Netmask: net.CIDRMask(24, 32),
	})
	if err != nil {
		t.Fatalf("create utun: %v", err)
	}
	defer dev.Close()

	kr := NewKernelRoutes(dev.Name())
	if err := kr.RefreshLink(); err != nil {
		t.Fatalf("RefreshLink: %v", err)
	}
	if !kr.LinkExists() {
		t.Fatal("LinkExists = false after RefreshLink")
	}

	_, dst, _ := net.ParseCIDR("10.123.45.0/24")
	r := Route{Destination: dst, Interface: dev.Name(), Enabled: true}

	added, err := kr.Add(r)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !added {
		t.Error("Add returned added=false for a new route")
	}
	defer kr.Remove(r)

	// Idempotent: adding again should report added=false.
	if again, err := kr.Add(r); err != nil || again {
		t.Errorf("second Add = (%v, %v), want (false, nil)", again, err)
	}

	exists, err := kr.RouteExists(dst)
	if err != nil || !exists {
		t.Errorf("RouteExists = (%v, %v), want (true, nil)", exists, err)
	}

	routes, err := kr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !containsDest(routes, "10.123.45.0/24") {
		t.Errorf("List missing 10.123.45.0/24; got %v", routes)
	}

	lookup, err := Lookup(net.ParseIP("10.123.45.9"), dev.Name())
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !lookup.IsMutiauk {
		t.Errorf("Lookup.IsMutiauk = false, interface=%q want %q", lookup.Interface, dev.Name())
	}

	removed, err := kr.Remove(r)
	if err != nil || !removed {
		t.Errorf("Remove = (%v, %v), want (true, nil)", removed, err)
	}
	if exists, _ := kr.RouteExists(dst); exists {
		t.Error("RouteExists = true after Remove")
	}
}

func containsDest(routes []Route, cidr string) bool {
	for _, r := range routes {
		if r.Destination != nil && r.Destination.String() == cidr {
			return true
		}
	}
	return false
}
