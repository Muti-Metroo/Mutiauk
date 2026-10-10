package autoroutes

import (
	"testing"
)

func TestFilterRoutes(t *testing.T) {
	tests := []struct {
		name      string
		routes    []DashboardRoute
		tunName   string
		wantCount int
		wantCIDRs []string
	}{
		{
			name: "filters domain routes",
			routes: []DashboardRoute{
				{Network: "10.10.0.0/16", RouteType: "cidr", OriginID: "abc"},
				{Network: "example.com", RouteType: "domain", OriginID: "def"},
				{Network: "*.example.com", RouteType: "domain", OriginID: "ghi"},
			},
			tunName:   "tun0",
			wantCount: 1,
			wantCIDRs: []string{"10.10.0.0/16"},
		},
		{
			name: "filters default routes",
			routes: []DashboardRoute{
				{Network: "10.10.0.0/16", RouteType: "cidr", OriginID: "abc"},
				{Network: "0.0.0.0/0", RouteType: "cidr", OriginID: "def"},
				{Network: "::/0", RouteType: "cidr", OriginID: "ghi"},
			},
			tunName:   "tun0",
			wantCount: 1,
			wantCIDRs: []string{"10.10.0.0/16"},
		},
		{
			name: "filters loopback routes",
			routes: []DashboardRoute{
				{Network: "10.10.0.0/16", RouteType: "cidr", OriginID: "abc"},
				{Network: "127.0.0.0/8", RouteType: "cidr", OriginID: "def"},
				{Network: "127.0.0.1/8", RouteType: "cidr", OriginID: "ghi"}, // normalizes to 127.0.0.0/8
				{Network: "::1/128", RouteType: "cidr", OriginID: "jkl"},
			},
			tunName:   "tun0",
			wantCount: 1,
			wantCIDRs: []string{"10.10.0.0/16"},
		},
		{
			name: "filters link-local routes",
			routes: []DashboardRoute{
				{Network: "10.10.0.0/16", RouteType: "cidr", OriginID: "abc"},
				{Network: "169.254.0.0/16", RouteType: "cidr", OriginID: "def"},
				{Network: "fe80::/10", RouteType: "cidr", OriginID: "ghi"},
			},
			tunName:   "tun0",
			wantCount: 1,
			wantCIDRs: []string{"10.10.0.0/16"},
		},
		{
			name: "filters multicast routes",
			routes: []DashboardRoute{
				{Network: "10.10.0.0/16", RouteType: "cidr", OriginID: "abc"},
				{Network: "224.0.0.0/4", RouteType: "cidr", OriginID: "def"},
				{Network: "ff00::/8", RouteType: "cidr", OriginID: "ghi"},
			},
			tunName:   "tun0",
			wantCount: 1,
			wantCIDRs: []string{"10.10.0.0/16"},
		},
		{
			name: "filters broadcast route",
			routes: []DashboardRoute{
				{Network: "10.10.0.0/16", RouteType: "cidr", OriginID: "abc"},
				{Network: "255.255.255.255/32", RouteType: "cidr", OriginID: "def"},
			},
			tunName:   "tun0",
			wantCount: 1,
			wantCIDRs: []string{"10.10.0.0/16"},
		},
		{
			name: "filters invalid CIDR",
			routes: []DashboardRoute{
				{Network: "10.10.0.0/16", RouteType: "cidr", OriginID: "abc"},
				{Network: "invalid-cidr", RouteType: "cidr", OriginID: "def"},
				{Network: "256.256.256.256/32", RouteType: "cidr", OriginID: "ghi"},
			},
			tunName:   "tun0",
			wantCount: 1,
			wantCIDRs: []string{"10.10.0.0/16"},
		},
		{
			name: "accepts valid CIDR routes",
			routes: []DashboardRoute{
				{Network: "10.10.0.0/16", RouteType: "cidr", OriginID: "abc"},
				{Network: "192.168.1.0/24", RouteType: "cidr", OriginID: "def"},
				{Network: "1.2.3.4/32", RouteType: "cidr", OriginID: "ghi"},
				{Network: "2001:db8::/32", RouteType: "cidr", OriginID: "jkl"},
			},
			tunName:   "tun0",
			wantCount: 4,
			wantCIDRs: []string{"10.10.0.0/16", "192.168.1.0/24", "1.2.3.4/32", "2001:db8::/32"},
		},
		{
			name:      "empty routes",
			routes:    []DashboardRoute{},
			tunName:   "tun0",
			wantCount: 0,
			wantCIDRs: []string{},
		},
		{
			name: "preserves origin ID in comment",
			routes: []DashboardRoute{
				{Network: "10.10.0.0/16", RouteType: "cidr", OriginID: "agent123"},
			},
			tunName:   "tun0",
			wantCount: 1,
			wantCIDRs: []string{"10.10.0.0/16"},
		},
		{
			name: "filters all unsafe routes together",
			routes: []DashboardRoute{
				{Network: "8.8.8.8/32", RouteType: "cidr", OriginID: "valid"},
				{Network: "0.0.0.0/0", RouteType: "cidr", OriginID: "default"},
				{Network: "::/0", RouteType: "cidr", OriginID: "default6"},
				{Network: "127.0.0.0/8", RouteType: "cidr", OriginID: "loopback"},
				{Network: "::1/128", RouteType: "cidr", OriginID: "loopback6"},
				{Network: "169.254.0.0/16", RouteType: "cidr", OriginID: "linklocal"},
				{Network: "fe80::/10", RouteType: "cidr", OriginID: "linklocal6"},
				{Network: "224.0.0.0/4", RouteType: "cidr", OriginID: "multicast"},
				{Network: "ff00::/8", RouteType: "cidr", OriginID: "multicast6"},
				{Network: "255.255.255.255/32", RouteType: "cidr", OriginID: "broadcast"},
			},
			tunName:   "tun0",
			wantCount: 1,
			wantCIDRs: []string{"8.8.8.8/32"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FilterRoutes(tt.routes, tt.tunName)

			if len(result) != tt.wantCount {
				t.Errorf("expected %d routes, got %d", tt.wantCount, len(result))
			}

			for i, r := range result {
				if r.Route.Interface != tt.tunName {
					t.Errorf("route %d: expected interface %s, got %s", i, tt.tunName, r.Route.Interface)
				}
				if !r.Route.Enabled {
					t.Errorf("route %d: expected enabled=true", i)
				}
			}

			// Check that expected CIDRs are present
			for _, wantCIDR := range tt.wantCIDRs {
				found := false
				for _, r := range result {
					if r.Route.Destination.String() == wantCIDR {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected CIDR %s not found in result", wantCIDR)
				}
			}
		})
	}
}

func TestFilterRoutes_OriginIDPreserved(t *testing.T) {
	routes := []DashboardRoute{
		{Network: "10.10.0.0/16", RouteType: "cidr", OriginID: "test-agent-123"},
	}

	result := FilterRoutes(routes, "tun0")

	if len(result) != 1 {
		t.Fatalf("expected 1 route, got %d", len(result))
	}

	if result[0].OriginID != "test-agent-123" {
		t.Errorf("expected OriginID 'test-agent-123', got '%s'", result[0].OriginID)
	}

	expectedComment := "autoroute:test-agent-123"
	if result[0].Route.Comment != expectedComment {
		t.Errorf("expected comment '%s', got '%s'", expectedComment, result[0].Route.Comment)
	}
}
