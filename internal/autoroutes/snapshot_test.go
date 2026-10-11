package autoroutes

import (
	"encoding/json"
	"net"
	"testing"
)

func snapshotBytes(t *testing.T, routes []DashboardRoute) []byte {
	t.Helper()
	b, err := json.Marshal(DashboardResponse{Routes: routes})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	return b
}

func TestLookupPathFromSnapshot(t *testing.T) {
	tests := []struct {
		name       string
		routes     []DashboardRoute
		lookupIP   string
		wantMatch  bool
		wantNet    string
		wantOrigin string
	}{
		{
			name: "exact match",
			routes: []DashboardRoute{
				{Network: "10.10.0.0/16", RouteType: "cidr", Origin: "agent-a", OriginID: "abc123"},
			},
			lookupIP:   "10.10.5.5",
			wantMatch:  true,
			wantNet:    "10.10.0.0/16",
			wantOrigin: "agent-a",
		},
		{
			name: "longest prefix match",
			routes: []DashboardRoute{
				{Network: "10.0.0.0/8", RouteType: "cidr", Origin: "agent-a", OriginID: "abc"},
				{Network: "10.10.0.0/16", RouteType: "cidr", Origin: "agent-b", OriginID: "def"},
				{Network: "10.10.5.0/24", RouteType: "cidr", Origin: "agent-c", OriginID: "ghi"},
			},
			lookupIP:   "10.10.5.100",
			wantMatch:  true,
			wantNet:    "10.10.5.0/24",
			wantOrigin: "agent-c",
		},
		{
			name: "no match",
			routes: []DashboardRoute{
				{Network: "10.10.0.0/16", RouteType: "cidr", Origin: "agent-a", OriginID: "abc"},
			},
			lookupIP:  "192.168.1.1",
			wantMatch: false,
		},
		{
			name: "skips domain routes",
			routes: []DashboardRoute{
				{Network: "example.com", RouteType: "domain", Origin: "agent-a", OriginID: "abc"},
				{Network: "10.10.0.0/16", RouteType: "cidr", Origin: "agent-b", OriginID: "def"},
			},
			lookupIP:   "10.10.1.1",
			wantMatch:  true,
			wantNet:    "10.10.0.0/16",
			wantOrigin: "agent-b",
		},
		{
			name: "skips invalid CIDR",
			routes: []DashboardRoute{
				{Network: "invalid-cidr", RouteType: "cidr", Origin: "agent-a", OriginID: "abc"},
				{Network: "10.10.0.0/16", RouteType: "cidr", Origin: "agent-b", OriginID: "def"},
			},
			lookupIP:   "10.10.1.1",
			wantMatch:  true,
			wantNet:    "10.10.0.0/16",
			wantOrigin: "agent-b",
		},
		{
			name:      "empty routes",
			routes:    []DashboardRoute{},
			lookupIP:  "10.10.1.1",
			wantMatch: false,
		},
		{
			name: "IPv6 match",
			routes: []DashboardRoute{
				{Network: "2001:db8::/32", RouteType: "cidr", Origin: "agent-a", OriginID: "abc"},
			},
			lookupIP:   "2001:db8::1",
			wantMatch:  true,
			wantNet:    "2001:db8::/32",
			wantOrigin: "agent-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := LookupPathFromSnapshot(snapshotBytes(t, tt.routes), net.ParseIP(tt.lookupIP))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantMatch {
				if result == nil {
					t.Fatal("expected match, got nil")
				}
				if result.Network != tt.wantNet {
					t.Errorf("Network = %v, want %v", result.Network, tt.wantNet)
				}
				if result.Origin != tt.wantOrigin {
					t.Errorf("Origin = %v, want %v", result.Origin, tt.wantOrigin)
				}
			} else if result != nil {
				t.Errorf("expected no match, got %+v", result)
			}
		})
	}
}

func TestLookupPathFromSnapshot_PreservesPathInfo(t *testing.T) {
	snapshot := snapshotBytes(t, []DashboardRoute{
		{
			Network:     "10.10.0.0/16",
			RouteType:   "cidr",
			Origin:      "agent-target",
			OriginID:    "target123",
			HopCount:    3,
			PathDisplay: []string{"local-agent", "relay-agent", "agent-target"},
			PathIDs:     []string{"local", "relay", "target123"},
		},
	})

	result, err := LookupPathFromSnapshot(snapshot, net.ParseIP("10.10.5.5"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if result.HopCount != 3 {
		t.Errorf("HopCount = %d, want 3", result.HopCount)
	}
	if len(result.PathDisplay) != 3 {
		t.Errorf("PathDisplay length = %d, want 3", len(result.PathDisplay))
	}
	if len(result.PathIDs) != 3 {
		t.Errorf("PathIDs length = %d, want 3", len(result.PathIDs))
	}
	if result.OriginID != "target123" {
		t.Errorf("OriginID = %s, want target123", result.OriginID)
	}
}

func TestLookupPathFromSnapshot_InvalidJSON(t *testing.T) {
	if _, err := LookupPathFromSnapshot([]byte("not json"), net.ParseIP("10.10.1.1")); err == nil {
		t.Error("expected error for invalid snapshot JSON, got nil")
	}
}
