package autoroutes

import (
	"net"
	"testing"

	"github.com/postalsys/mutiauk/internal/route"
)

func mustParseCIDR(s string) *net.IPNet {
	_, ipNet, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return ipNet
}

func TestResolveConflicts(t *testing.T) {
	tests := []struct {
		name      string
		routes    []AutoRoute
		wantCount int
		wantCIDRs []string
	}{
		{
			name: "no duplicates",
			routes: []AutoRoute{
				{
					Route:    route.Route{Destination: mustParseCIDR("10.10.0.0/16"), Interface: "tun0"},
					OriginID: "abc",
				},
				{
					Route:    route.Route{Destination: mustParseCIDR("10.11.0.0/16"), Interface: "tun0"},
					OriginID: "def",
				},
			},
			wantCount: 2,
			wantCIDRs: []string{"10.10.0.0/16", "10.11.0.0/16"},
		},
		{
			name: "duplicate networks - picks smallest originID",
			routes: []AutoRoute{
				{
					Route:    route.Route{Destination: mustParseCIDR("10.10.0.0/16"), Interface: "tun0"},
					OriginID: "zzz",
				},
				{
					Route:    route.Route{Destination: mustParseCIDR("10.10.0.0/16"), Interface: "tun0"},
					OriginID: "aaa",
				},
				{
					Route:    route.Route{Destination: mustParseCIDR("10.10.0.0/16"), Interface: "tun0"},
					OriginID: "mmm",
				},
			},
			wantCount: 1,
			wantCIDRs: []string{"10.10.0.0/16"},
		},
		{
			name: "mixed duplicates and unique",
			routes: []AutoRoute{
				{
					Route:    route.Route{Destination: mustParseCIDR("10.10.0.0/16"), Interface: "tun0"},
					OriginID: "bbb",
				},
				{
					Route:    route.Route{Destination: mustParseCIDR("10.10.0.0/16"), Interface: "tun0"},
					OriginID: "aaa",
				},
				{
					Route:    route.Route{Destination: mustParseCIDR("10.11.0.0/16"), Interface: "tun0"},
					OriginID: "ccc",
				},
			},
			wantCount: 2,
			wantCIDRs: []string{"10.10.0.0/16", "10.11.0.0/16"},
		},
		{
			name:      "empty input",
			routes:    []AutoRoute{},
			wantCount: 0,
			wantCIDRs: []string{},
		},
		{
			name:      "nil input",
			routes:    nil,
			wantCount: 0,
			wantCIDRs: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ResolveConflicts(tt.routes)

			if len(result) != tt.wantCount {
				t.Errorf("expected %d routes, got %d", tt.wantCount, len(result))
			}

			// Check expected CIDRs are present
			for _, wantCIDR := range tt.wantCIDRs {
				found := false
				for _, r := range result {
					if r.Destination.String() == wantCIDR {
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

func TestResolveConflicts_DeterministicChoice(t *testing.T) {
	// Run multiple times to verify determinism
	for i := 0; i < 10; i++ {
		routes := []AutoRoute{
			{
				Route:    route.Route{Destination: mustParseCIDR("10.10.0.0/16"), Interface: "tun0", Comment: "from-zzz"},
				OriginID: "zzz",
			},
			{
				Route:    route.Route{Destination: mustParseCIDR("10.10.0.0/16"), Interface: "tun0", Comment: "from-aaa"},
				OriginID: "aaa",
			},
		}

		result := ResolveConflicts(routes)

		if len(result) != 1 {
			t.Fatalf("expected 1 route, got %d", len(result))
		}

		// Should always pick the route from "aaa" (smallest OriginID)
		if result[0].Comment != "from-aaa" {
			t.Errorf("iteration %d: expected route from 'aaa', got comment '%s'", i, result[0].Comment)
		}
	}
}

func TestMergeRoutes(t *testing.T) {
	tests := []struct {
		name         string
		configRoutes []route.Route
		autoRoutes   []route.Route
		wantCount    int
		wantCIDRs    []string
	}{
		{
			name: "no overlap",
			configRoutes: []route.Route{
				{Destination: mustParseCIDR("10.0.0.0/8"), Interface: "tun0"},
			},
			autoRoutes: []route.Route{
				{Destination: mustParseCIDR("192.168.0.0/16"), Interface: "tun0"},
			},
			wantCount: 2,
			wantCIDRs: []string{"10.0.0.0/8", "192.168.0.0/16"},
		},
		{
			name: "config takes precedence",
			configRoutes: []route.Route{
				{Destination: mustParseCIDR("10.10.0.0/16"), Interface: "tun0", Comment: "config"},
			},
			autoRoutes: []route.Route{
				{Destination: mustParseCIDR("10.10.0.0/16"), Interface: "tun0", Comment: "auto"},
			},
			wantCount: 1,
			wantCIDRs: []string{"10.10.0.0/16"},
		},
		{
			name:         "empty config routes",
			configRoutes: []route.Route{},
			autoRoutes: []route.Route{
				{Destination: mustParseCIDR("10.10.0.0/16"), Interface: "tun0"},
			},
			wantCount: 1,
			wantCIDRs: []string{"10.10.0.0/16"},
		},
		{
			name: "empty auto routes",
			configRoutes: []route.Route{
				{Destination: mustParseCIDR("10.0.0.0/8"), Interface: "tun0"},
			},
			autoRoutes: []route.Route{},
			wantCount:  1,
			wantCIDRs:  []string{"10.0.0.0/8"},
		},
		{
			name:         "both empty",
			configRoutes: []route.Route{},
			autoRoutes:   []route.Route{},
			wantCount:    0,
			wantCIDRs:    []string{},
		},
		{
			name: "multiple overlaps",
			configRoutes: []route.Route{
				{Destination: mustParseCIDR("10.10.0.0/16"), Interface: "tun0", Comment: "config1"},
				{Destination: mustParseCIDR("10.11.0.0/16"), Interface: "tun0", Comment: "config2"},
			},
			autoRoutes: []route.Route{
				{Destination: mustParseCIDR("10.10.0.0/16"), Interface: "tun0", Comment: "auto1"},
				{Destination: mustParseCIDR("10.11.0.0/16"), Interface: "tun0", Comment: "auto2"},
				{Destination: mustParseCIDR("10.12.0.0/16"), Interface: "tun0", Comment: "auto3"},
			},
			wantCount: 3,
			wantCIDRs: []string{"10.10.0.0/16", "10.11.0.0/16", "10.12.0.0/16"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MergeRoutes(tt.configRoutes, tt.autoRoutes)

			if len(result) != tt.wantCount {
				t.Errorf("expected %d routes, got %d", tt.wantCount, len(result))
			}

			// Check expected CIDRs are present
			for _, wantCIDR := range tt.wantCIDRs {
				found := false
				for _, r := range result {
					if r.Destination.String() == wantCIDR {
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

func TestMergeRoutes_ConfigPreserved(t *testing.T) {
	configRoutes := []route.Route{
		{Destination: mustParseCIDR("10.10.0.0/16"), Interface: "tun0", Comment: "config-route"},
	}
	autoRoutes := []route.Route{
		{Destination: mustParseCIDR("10.10.0.0/16"), Interface: "tun0", Comment: "auto-route"},
	}

	result := MergeRoutes(configRoutes, autoRoutes)

	if len(result) != 1 {
		t.Fatalf("expected 1 route, got %d", len(result))
	}

	// Config route should be preserved
	if result[0].Comment != "config-route" {
		t.Errorf("expected config route to be preserved, got comment '%s'", result[0].Comment)
	}
}

func TestRouteContains(t *testing.T) {
	tests := []struct {
		a, b     string
		contains bool
	}{
		{"10.0.0.0/8", "10.10.0.0/16", true},
		{"10.0.0.0/8", "10.0.0.0/8", true},
		{"10.10.0.0/16", "10.0.0.0/8", false},
		{"10.10.0.0/16", "10.11.0.0/16", false},
		{"192.168.0.0/16", "10.10.0.0/16", false},
		// IPv6
		{"2001:db8::/32", "2001:db8:1::/48", true},
		{"2001:db8:1::/48", "2001:db8::/32", false},
		// Different families
		{"10.0.0.0/8", "2001:db8::/32", false},
	}

	for _, tt := range tests {
		t.Run(tt.a+"_contains_"+tt.b, func(t *testing.T) {
			a := mustParseCIDR(tt.a)
			b := mustParseCIDR(tt.b)

			result := routeContains(a, b)
			if result != tt.contains {
				t.Errorf("routeContains(%s, %s) = %v, want %v", tt.a, tt.b, result, tt.contains)
			}
		})
	}
}

func TestExtractOriginID(t *testing.T) {
	tests := []struct {
		name    string
		comment string
		want    string
	}{
		{
			name:    "valid autoroute comment",
			comment: "autoroute:abc123",
			want:    "abc123",
		},
		{
			name:    "valid autoroute with long ID",
			comment: "autoroute:agent-12345-abcdef",
			want:    "agent-12345-abcdef",
		},
		{
			name:    "empty origin ID",
			comment: "autoroute:",
			want:    "",
		},
		{
			name:    "non-autoroute comment",
			comment: "manual route",
			want:    "",
		},
		{
			name:    "empty comment",
			comment: "",
			want:    "",
		},
		{
			name:    "partial prefix",
			comment: "autoroute",
			want:    "",
		},
		{
			name:    "similar but different prefix",
			comment: "autoroutes:abc123",
			want:    "",
		},
		{
			name:    "prefix in middle of string",
			comment: "some autoroute:abc123",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := route.Route{Comment: tt.comment}
			result := extractOriginID(r)
			if result != tt.want {
				t.Errorf("extractOriginID(%q) = %q, want %q", tt.comment, result, tt.want)
			}
		})
	}
}

func TestMergeRoutes_NilDestination(t *testing.T) {
	// Test that routes with nil destinations are handled gracefully
	configRoutes := []route.Route{
		{Destination: mustParseCIDR("10.10.0.0/16"), Interface: "tun0"},
	}
	autoRoutes := []route.Route{
		{Destination: nil, Interface: "tun0", Comment: "invalid"},
		{Destination: mustParseCIDR("10.11.0.0/16"), Interface: "tun0"},
	}

	result := MergeRoutes(configRoutes, autoRoutes)

	// Should have config route + valid auto route (nil destination skipped)
	if len(result) != 2 {
		t.Errorf("expected 2 routes, got %d", len(result))
	}
}

func TestMergeRoutes_NilConfigDestination(t *testing.T) {
	// Test that config routes with nil destinations don't panic
	configRoutes := []route.Route{
		{Destination: nil, Interface: "tun0", Comment: "invalid config"},
		{Destination: mustParseCIDR("10.10.0.0/16"), Interface: "tun0"},
	}
	autoRoutes := []route.Route{
		{Destination: mustParseCIDR("10.11.0.0/16"), Interface: "tun0"},
	}

	result := MergeRoutes(configRoutes, autoRoutes)

	// Should have both config routes + auto route
	if len(result) != 3 {
		t.Errorf("expected 3 routes, got %d", len(result))
	}
}
