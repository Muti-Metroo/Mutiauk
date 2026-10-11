package daemon

import (
	"testing"

	"github.com/postalsys/mutiauk/internal/config"
)

// TestConfigRouteHelpersDoNotAliasInput is the regression guard for a data
// race: AddRoute/RemoveRoute used to splice the Routes slice of the live
// stored *config.Config in place, which raced with concurrent readers
// (GetRoutes/GetStatus load the same pointer). The helpers must return a
// fresh config whose Routes slice shares no backing storage with the input.
func TestConfigRouteHelpersDoNotAliasInput(t *testing.T) {
	cfg := &config.Config{}
	cfg.Routes = []config.RouteConfig{
		{Destination: "10.0.0.0/8", Comment: "a", Enabled: true},
		{Destination: "192.168.0.0/16", Comment: "b", Enabled: true},
	}

	added := configWithRouteAdded(cfg, "172.16.0.0/12", "c")
	if added == cfg {
		t.Fatal("configWithRouteAdded returned the input pointer")
	}
	if len(added.Routes) != 3 {
		t.Fatalf("added has %d routes, want 3", len(added.Routes))
	}
	if len(cfg.Routes) != 2 {
		t.Fatalf("input grew to %d routes, want unchanged 2", len(cfg.Routes))
	}
	// Writing through the result must not touch the input's backing array.
	added.Routes[0].Comment = "mutated"
	if cfg.Routes[0].Comment != "a" {
		t.Fatalf("input route comment = %q, want unchanged %q", cfg.Routes[0].Comment, "a")
	}

	removed := configWithRouteRemoved(cfg, "10.0.0.0/8")
	if removed == cfg {
		t.Fatal("configWithRouteRemoved returned the input pointer")
	}
	if len(removed.Routes) != 1 || removed.Routes[0].Destination != "192.168.0.0/16" {
		t.Fatalf("removed = %+v, want only 192.168.0.0/16", removed.Routes)
	}
	if len(cfg.Routes) != 2 || cfg.Routes[0].Destination != "10.0.0.0/8" || cfg.Routes[1].Destination != "192.168.0.0/16" {
		t.Fatalf("input mutated to %+v", cfg.Routes)
	}

	// Removing an unknown destination is a no-op but still must not alias.
	noop := configWithRouteRemoved(cfg, "203.0.113.0/24")
	if len(noop.Routes) != 2 {
		t.Fatalf("unknown removal dropped routes: %+v", noop.Routes)
	}
	noop.Routes[0].Comment = "mutated"
	if cfg.Routes[0].Comment != "a" {
		t.Fatal("no-op removal aliases the input slice")
	}
}
