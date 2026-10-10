package daemon

import (
	"net"
	"testing"

	"go.uber.org/zap"

	"github.com/postalsys/mutiauk/internal/autoroutes"
)

func TestDashboardHostsToMeshHosts(t *testing.T) {
	logger := zap.NewNop()

	in := []autoroutes.DashboardMeshHost{
		{Name: "good-v4.mesh", IPv4: "10.0.0.1"},
		{Name: "good-v6.mesh", IPv6: "fd00::1"},
		{Name: "good-both.mesh", IPv4: "10.0.0.2", IPv6: "fd00::2"},
		{Name: "bad-v4.mesh", IPv4: "not-an-ip"},
		{Name: "bad-v4-actually-v6.mesh", IPv4: "fd00::ff"}, // IPv4 field carrying a v6 literal
		{Name: "bad-v6.mesh", IPv6: "10.0.0.3"},             // IPv6 field carrying a v4 literal
		{Name: "no-ips.mesh"},
	}

	got := dashboardHostsToMeshHosts(in, logger)

	want := map[string]struct {
		ipv4 string
		ipv6 string
	}{
		"good-v4.mesh":   {ipv4: "10.0.0.1"},
		"good-v6.mesh":   {ipv6: "fd00::1"},
		"good-both.mesh": {ipv4: "10.0.0.2", ipv6: "fd00::2"},
	}

	if len(got) != len(want) {
		t.Fatalf("got %d hosts, want %d (%v)", len(got), len(want), got)
	}

	for _, h := range got {
		exp, ok := want[h.Name]
		if !ok {
			t.Errorf("unexpected host %q in output", h.Name)
			continue
		}
		if exp.ipv4 != "" {
			if h.IPv4 == nil || !h.IPv4.Equal(net.ParseIP(exp.ipv4)) {
				t.Errorf("%s ipv4=%v, want %s", h.Name, h.IPv4, exp.ipv4)
			}
		} else if h.IPv4 != nil {
			t.Errorf("%s unexpected ipv4=%v", h.Name, h.IPv4)
		}
		if exp.ipv6 != "" {
			if h.IPv6 == nil || !h.IPv6.Equal(net.ParseIP(exp.ipv6)) {
				t.Errorf("%s ipv6=%v, want %s", h.Name, h.IPv6, exp.ipv6)
			}
		} else if h.IPv6 != nil {
			t.Errorf("%s unexpected ipv6=%v", h.Name, h.IPv6)
		}
	}
}

func TestDashboardHostsToMeshHosts_Empty(t *testing.T) {
	if got := dashboardHostsToMeshHosts(nil, zap.NewNop()); len(got) != 0 {
		t.Errorf("nil input -> %d hosts, want 0", len(got))
	}
}
