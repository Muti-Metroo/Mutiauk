package route

import (
	"net"
	"strings"
	"testing"
)

// Helper to create *net.IPNet from CIDR string
func mustParseCIDR(s string) *net.IPNet {
	_, ipNet, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return ipNet
}

// --- Plan Tests ---

func TestPlan_IsEmpty(t *testing.T) {
	tests := []struct {
		name  string
		plan  Plan
		empty bool
	}{
		{
			name:  "empty plan",
			plan:  Plan{},
			empty: true,
		},
		{
			name:  "nil slices",
			plan:  Plan{ToAdd: nil, ToRemove: nil},
			empty: true,
		},
		{
			name:  "empty slices",
			plan:  Plan{ToAdd: []Route{}, ToRemove: []Route{}},
			empty: true,
		},
		{
			name: "has additions",
			plan: Plan{
				ToAdd: []Route{{Destination: mustParseCIDR("10.0.0.0/8")}},
			},
			empty: false,
		},
		{
			name: "has removals",
			plan: Plan{
				ToRemove: []Route{{Destination: mustParseCIDR("10.0.0.0/8")}},
			},
			empty: false,
		},
		{
			name: "has both",
			plan: Plan{
				ToAdd:    []Route{{Destination: mustParseCIDR("10.0.0.0/8")}},
				ToRemove: []Route{{Destination: mustParseCIDR("192.168.0.0/16")}},
			},
			empty: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.plan.IsEmpty()
			if result != tt.empty {
				t.Errorf("IsEmpty() = %v, want %v", result, tt.empty)
			}
		})
	}
}

func TestPlan_String(t *testing.T) {
	tests := []struct {
		name     string
		plan     Plan
		contains []string
	}{
		{
			name:     "empty plan",
			plan:     Plan{},
			contains: []string{"No changes"},
		},
		{
			name: "additions only",
			plan: Plan{
				ToAdd: []Route{
					{Destination: mustParseCIDR("10.0.0.0/8")},
					{Destination: mustParseCIDR("192.168.0.0/16")},
				},
			},
			contains: []string{"Add 2 route(s)", "+ 10.0.0.0/8", "+ 192.168.0.0/16"},
		},
		{
			name: "removals only",
			plan: Plan{
				ToRemove: []Route{
					{Destination: mustParseCIDR("172.16.0.0/12")},
				},
			},
			contains: []string{"Remove 1 route(s)", "- 172.16.0.0/12"},
		},
		{
			name: "both additions and removals",
			plan: Plan{
				ToAdd:    []Route{{Destination: mustParseCIDR("10.0.0.0/8")}},
				ToRemove: []Route{{Destination: mustParseCIDR("192.168.0.0/16")}},
			},
			contains: []string{"Add 1 route(s)", "+ 10.0.0.0/8", "Remove 1 route(s)", "- 192.168.0.0/16"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.plan.String()
			for _, want := range tt.contains {
				if !strings.Contains(result, want) {
					t.Errorf("String() = %q, want to contain %q", result, want)
				}
			}
		})
	}
}

// --- Conflict Tests ---

func TestConflict_String(t *testing.T) {
	conflict := Conflict{
		Existing: Route{Destination: mustParseCIDR("10.0.0.0/8")},
		Proposed: Route{Destination: mustParseCIDR("10.10.0.0/16")},
		Reason:   "duplicate route",
	}

	result := conflict.String()

	if !strings.Contains(result, "10.10.0.0/16") {
		t.Errorf("String() should contain proposed destination")
	}
	if !strings.Contains(result, "10.0.0.0/8") {
		t.Errorf("String() should contain existing destination")
	}
	if !strings.Contains(result, "duplicate route") {
		t.Errorf("String() should contain reason")
	}
}

// --- ValidateRoutes Tests ---

func TestValidateRoutes(t *testing.T) {
	tests := []struct {
		name       string
		routes     []Route
		wantErrors int
		errContain string
	}{
		{
			name: "valid routes",
			routes: []Route{
				{Destination: mustParseCIDR("10.0.0.0/8")},
				{Destination: mustParseCIDR("192.168.0.0/16")},
			},
			wantErrors: 0,
		},
		{
			name: "nil destination",
			routes: []Route{
				{Destination: nil},
			},
			wantErrors: 1,
			errContain: "destination is required",
		},
		{
			name: "very broad IPv4 route",
			routes: []Route{
				{Destination: mustParseCIDR("10.0.0.0/4")}, // /4 is very broad
			},
			wantErrors: 1,
			errContain: "very broad IPv4",
		},
		{
			name: "very broad IPv6 route",
			routes: []Route{
				{Destination: mustParseCIDR("2001:db8::/8")}, // /8 is very broad for IPv6
			},
			wantErrors: 1,
			errContain: "very broad IPv6",
		},
		{
			name: "IPv4 gateway with IPv6 destination",
			routes: []Route{
				{
					Destination: mustParseCIDR("2001:db8::/32"),
					Gateway:     net.ParseIP("192.168.1.1"),
				},
			},
			wantErrors: 1,
			errContain: "IPv4 gateway with IPv6 destination",
		},
		{
			name: "IPv6 gateway with IPv4 destination",
			routes: []Route{
				{
					Destination: mustParseCIDR("10.0.0.0/8"),
					Gateway:     net.ParseIP("2001:db8::1"),
				},
			},
			wantErrors: 1,
			errContain: "IPv6 gateway with IPv4 destination",
		},
		{
			name: "multiple errors",
			routes: []Route{
				{Destination: nil},
				{Destination: mustParseCIDR("10.0.0.0/4")},
			},
			wantErrors: 2,
		},
		{
			name:       "empty routes",
			routes:     []Route{},
			wantErrors: 0,
		},
		{
			name: "valid route with matching gateway family",
			routes: []Route{
				{
					Destination: mustParseCIDR("10.0.0.0/8"),
					Gateway:     net.ParseIP("192.168.1.1"),
				},
				{
					Destination: mustParseCIDR("2001:db8::/32"),
					Gateway:     net.ParseIP("2001:db8::1"),
				},
			},
			wantErrors: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errors := ValidateRoutes(tt.routes)

			if len(errors) != tt.wantErrors {
				t.Errorf("ValidateRoutes() returned %d errors, want %d", len(errors), tt.wantErrors)
				for _, err := range errors {
					t.Logf("  error: %v", err)
				}
			}

			if tt.errContain != "" && len(errors) > 0 {
				found := false
				for _, err := range errors {
					if strings.Contains(err.Error(), tt.errContain) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected error containing %q", tt.errContain)
				}
			}
		})
	}
}

// --- routeContains Tests ---

func TestRouteContains(t *testing.T) {
	tests := []struct {
		name     string
		a, b     string
		contains bool
	}{
		{
			name:     "larger contains smaller",
			a:        "10.0.0.0/8",
			b:        "10.10.0.0/16",
			contains: true,
		},
		{
			name:     "same network contains itself",
			a:        "10.0.0.0/8",
			b:        "10.0.0.0/8",
			contains: true,
		},
		{
			name:     "smaller does not contain larger",
			a:        "10.10.0.0/16",
			b:        "10.0.0.0/8",
			contains: false,
		},
		{
			name:     "non-overlapping networks",
			a:        "10.0.0.0/8",
			b:        "192.168.0.0/16",
			contains: false,
		},
		{
			name:     "sibling networks",
			a:        "10.10.0.0/16",
			b:        "10.11.0.0/16",
			contains: false,
		},
		{
			name:     "IPv6 larger contains smaller",
			a:        "2001:db8::/32",
			b:        "2001:db8:1::/48",
			contains: true,
		},
		{
			name:     "IPv6 smaller does not contain larger",
			a:        "2001:db8:1::/48",
			b:        "2001:db8::/32",
			contains: false,
		},
		{
			name:     "different address families",
			a:        "10.0.0.0/8",
			b:        "2001:db8::/32",
			contains: false,
		},
		{
			name:     "host route contained in network",
			a:        "10.0.0.0/8",
			b:        "10.1.2.3/32",
			contains: true,
		},
		{
			name:     "nested containment",
			a:        "10.0.0.0/8",
			b:        "10.10.10.0/24",
			contains: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := mustParseCIDR(tt.a)
			b := mustParseCIDR(tt.b)

			result := routeContains(a, b)
			if result != tt.contains {
				t.Errorf("routeContains(%s, %s) = %v, want %v", tt.a, tt.b, result, tt.contains)
			}
		})
	}
}

func TestRouteContains_NilHandling(t *testing.T) {
	a := mustParseCIDR("10.0.0.0/8")

	if routeContains(nil, a) {
		t.Error("routeContains(nil, a) should return false")
	}
	if routeContains(a, nil) {
		t.Error("routeContains(a, nil) should return false")
	}
	if routeContains(nil, nil) {
		t.Error("routeContains(nil, nil) should return false")
	}
}

// --- routeOverlaps Tests ---

func TestRouteOverlaps(t *testing.T) {
	tests := []struct {
		name     string
		a, b     string
		overlaps bool
	}{
		{
			name:     "larger contains smaller - overlaps",
			a:        "10.0.0.0/8",
			b:        "10.10.0.0/16",
			overlaps: true,
		},
		{
			name:     "smaller contained by larger - overlaps",
			a:        "10.10.0.0/16",
			b:        "10.0.0.0/8",
			overlaps: true,
		},
		{
			name:     "same network - overlaps",
			a:        "10.0.0.0/8",
			b:        "10.0.0.0/8",
			overlaps: true,
		},
		{
			name:     "non-overlapping networks",
			a:        "10.0.0.0/8",
			b:        "192.168.0.0/16",
			overlaps: false,
		},
		{
			name:     "sibling networks - no overlap",
			a:        "10.10.0.0/16",
			b:        "10.11.0.0/16",
			overlaps: false,
		},
		{
			name:     "different address families - no overlap",
			a:        "10.0.0.0/8",
			b:        "2001:db8::/32",
			overlaps: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := mustParseCIDR(tt.a)
			b := mustParseCIDR(tt.b)

			result := routeOverlaps(a, b)
			if result != tt.overlaps {
				t.Errorf("routeOverlaps(%s, %s) = %v, want %v", tt.a, tt.b, result, tt.overlaps)
			}
		})
	}
}

// --- LookupResult Tests ---

func TestLookupResult_Fields(t *testing.T) {
	result := LookupResult{
		Destination:  "example.com",
		ResolvedIP:   net.ParseIP("93.184.216.34"),
		MatchedRoute: mustParseCIDR("0.0.0.0/0"),
		Interface:    "eth0",
		Gateway:      net.ParseIP("192.168.1.1"),
		IsMutiauk:    false,
	}

	if result.Destination != "example.com" {
		t.Errorf("Destination = %v, want example.com", result.Destination)
	}
	if result.ResolvedIP.String() != "93.184.216.34" {
		t.Errorf("ResolvedIP = %v, want 93.184.216.34", result.ResolvedIP)
	}
	if result.Interface != "eth0" {
		t.Errorf("Interface = %v, want eth0", result.Interface)
	}
	if result.IsMutiauk {
		t.Error("IsMutiauk should be false")
	}
}

// --- Route struct Tests ---

func TestRoute_Fields(t *testing.T) {
	route := Route{
		Destination: mustParseCIDR("10.0.0.0/8"),
		Gateway:     net.ParseIP("192.168.1.1"),
		Interface:   "tun0",
		Metric:      100,
		Comment:     "test route",
		Enabled:     true,
	}

	if route.Destination.String() != "10.0.0.0/8" {
		t.Errorf("Destination = %v, want 10.0.0.0/8", route.Destination)
	}
	if route.Gateway.String() != "192.168.1.1" {
		t.Errorf("Gateway = %v, want 192.168.1.1", route.Gateway)
	}
	if route.Interface != "tun0" {
		t.Errorf("Interface = %v, want tun0", route.Interface)
	}
	if route.Metric != 100 {
		t.Errorf("Metric = %v, want 100", route.Metric)
	}
	if route.Comment != "test route" {
		t.Errorf("Comment = %v, want 'test route'", route.Comment)
	}
	if !route.Enabled {
		t.Error("Enabled should be true")
	}
}

// --- State Tests ---

func TestState_MapOperations(t *testing.T) {
	state := make(State)

	route1 := Route{
		Destination: mustParseCIDR("10.0.0.0/8"),
		Interface:   "tun0",
		Enabled:     true,
	}

	// Add route
	state["10.0.0.0/8"] = route1

	// Check retrieval
	if r, ok := state["10.0.0.0/8"]; !ok {
		t.Error("route should exist in state")
	} else if r.Interface != "tun0" {
		t.Errorf("Interface = %v, want tun0", r.Interface)
	}

	// Check non-existent
	if _, ok := state["192.168.0.0/16"]; ok {
		t.Error("route should not exist in state")
	}

	// Delete route
	delete(state, "10.0.0.0/8")
	if _, ok := state["10.0.0.0/8"]; ok {
		t.Error("route should be deleted from state")
	}
}

// --- Edge Cases ---

func TestValidateRoutes_BoundaryPrefixLengths(t *testing.T) {
	// Test boundary conditions for "very broad" detection
	tests := []struct {
		name      string
		cidr      string
		wantBroad bool
	}{
		// IPv4: /8 and above is OK, below /8 is "very broad"
		{"IPv4 /8 is OK", "10.0.0.0/8", false},
		{"IPv4 /7 is broad", "10.0.0.0/7", true},
		{"IPv4 /1 is broad", "0.0.0.0/1", true},
		// IPv6: /16 and above is OK, below /16 is "very broad"
		{"IPv6 /16 is OK", "2001:db8::/16", false},
		{"IPv6 /15 is broad", "2001:db8::/15", true},
		{"IPv6 /8 is broad", "2001:db8::/8", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routes := []Route{{Destination: mustParseCIDR(tt.cidr)}}
			errors := ValidateRoutes(routes)

			hasBroadError := false
			for _, err := range errors {
				if strings.Contains(err.Error(), "very broad") {
					hasBroadError = true
					break
				}
			}

			if hasBroadError != tt.wantBroad {
				t.Errorf("ValidateRoutes(%s) broad error = %v, want %v", tt.cidr, hasBroadError, tt.wantBroad)
			}
		})
	}
}
