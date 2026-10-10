package daemon

import (
	"net"
	"testing"

	"github.com/postalsys/mutiauk/internal/route"
)

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("ParseCIDR(%q): %v", s, err)
	}
	return n
}

func TestSocksServerHost(t *testing.T) {
	tests := []struct {
		server string
		want   string
	}{
		{"1.2.3.4:1080", "1.2.3.4"},
		{"agent.example.com:1080", "agent.example.com"},
		{"ws://1.2.3.4:8080/socks5", "1.2.3.4"},
		{"wss://agent.example.com/socks5", "agent.example.com"},
		{"[2001:db8::1]:1080", "2001:db8::1"},
		{"1.2.3.4", "1.2.3.4"}, // no port
	}
	for _, tt := range tests {
		if got := socksServerHost(tt.server); got != tt.want {
			t.Errorf("socksServerHost(%q) = %q, want %q", tt.server, got, tt.want)
		}
	}
}

func TestContainsProtectedIP(t *testing.T) {
	protected := []net.IP{net.ParseIP("1.2.3.4")}

	if ip := containsProtectedIP(mustCIDR(t, "1.2.3.0/24"), protected); ip == nil {
		t.Error("expected 1.2.3.0/24 to contain protected 1.2.3.4")
	}
	if ip := containsProtectedIP(mustCIDR(t, "1.2.3.4/32"), protected); ip == nil {
		t.Error("expected exact /32 to contain the protected IP")
	}
	if ip := containsProtectedIP(mustCIDR(t, "10.0.0.0/8"), protected); ip != nil {
		t.Errorf("did not expect 10.0.0.0/8 to contain 1.2.3.4, got %v", ip)
	}
	// A v6 route must not match a v4 protected IP.
	if ip := containsProtectedIP(mustCIDR(t, "2001:db8::/32"), protected); ip != nil {
		t.Errorf("v6 route should not contain v4 protected IP, got %v", ip)
	}
}

// TestSplitProtectedRoutes is the core guard: a route covering the SOCKS5
// server must be refused (not kept), while unrelated routes pass through.
func TestSplitProtectedRoutes(t *testing.T) {
	protected := []net.IP{net.ParseIP("1.2.3.4")}

	poison := route.Route{Destination: mustCIDR(t, "1.2.3.0/24"), Comment: "autoroute:abc", Enabled: true}
	safe := route.Route{Destination: mustCIDR(t, "10.0.0.0/8"), Comment: "autoroute:def", Enabled: true}

	autoSet := destSet([]route.Route{poison, safe})

	keep, refused := splitProtectedRoutes([]route.Route{poison, safe}, protected, autoSet, nil)

	if len(keep) != 1 || keep[0].Destination.String() != "10.0.0.0/8" {
		t.Fatalf("keep = %+v, want only 10.0.0.0/8", keep)
	}
	if len(refused) != 1 {
		t.Fatalf("refused count = %d, want 1", len(refused))
	}
	if refused[0].Destination != "1.2.3.0/24" {
		t.Errorf("refused destination = %q, want 1.2.3.0/24", refused[0].Destination)
	}
	if refused[0].Source != "auto" {
		t.Errorf("refused source = %q, want auto", refused[0].Source)
	}
	if refused[0].Reason == "" {
		t.Error("refused reason should be populated")
	}
}

func TestSplitProtectedRoutes_NoProtectedIPs(t *testing.T) {
	// With no protected IPs resolved (e.g. unresolved hostname), nothing is
	// refused -- routes pass through unchanged.
	r := route.Route{Destination: mustCIDR(t, "1.2.3.0/24"), Enabled: true}
	keep, refused := splitProtectedRoutes([]route.Route{r}, nil, nil, nil)
	if len(keep) != 1 || len(refused) != 0 {
		t.Errorf("keep=%d refused=%d, want keep=1 refused=0", len(keep), len(refused))
	}
}
