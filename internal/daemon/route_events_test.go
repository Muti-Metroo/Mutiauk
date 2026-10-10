package daemon

import (
	"net"
	"testing"

	"github.com/postalsys/mutiauk/internal/route"
)

func cidr(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("bad cidr %q: %v", s, err)
	}
	return n
}

func TestRouteEventLogRecord(t *testing.T) {
	var log routeEventLog
	log.record("added", "1.2.3.4/32", "auto", "autoroute:x")
	log.record("removed", "1.2.3.4/32", "auto", "")

	evs := log.snapshot()
	if len(evs) != 2 {
		t.Fatalf("len = %d, want 2", len(evs))
	}
	if evs[0].Seq != 1 || evs[1].Seq != 2 {
		t.Fatalf("seqs = %d,%d, want 1,2", evs[0].Seq, evs[1].Seq)
	}
	if evs[0].Action != "added" || evs[0].Destination != "1.2.3.4/32" {
		t.Fatalf("unexpected first event: %+v", evs[0])
	}
}

func TestRouteEventLogSuppressesConsecutiveDuplicates(t *testing.T) {
	var log routeEventLog
	log.record("removed", "10.0.0.1/32", "manual", "")
	log.record("removed", "10.0.0.1/32", "manual", "") // dup -> dropped
	log.record("added", "10.0.0.1/32", "manual", "")   // different action -> kept
	log.record("removed", "10.0.0.1/32", "manual", "") // not consecutive dup -> kept

	evs := log.snapshot()
	if len(evs) != 3 {
		t.Fatalf("len = %d, want 3 (one duplicate suppressed): %+v", len(evs), evs)
	}
}

func TestRouteEventLogCapacity(t *testing.T) {
	var log routeEventLog
	// Alternate destinations so consecutive-dup suppression never fires.
	for i := 0; i < routeEventCapacity+50; i++ {
		dst := "10.0.0.1/32"
		if i%2 == 1 {
			dst = "10.0.0.2/32"
		}
		log.record("added", dst, "manual", "")
	}
	evs := log.snapshot()
	if len(evs) != routeEventCapacity {
		t.Fatalf("len = %d, want %d", len(evs), routeEventCapacity)
	}
	// Sequence numbers keep climbing past the ring capacity.
	if evs[len(evs)-1].Seq != uint64(routeEventCapacity+50) {
		t.Fatalf("last seq = %d, want %d", evs[len(evs)-1].Seq, routeEventCapacity+50)
	}
}

func TestFilterOutDests(t *testing.T) {
	routes := []route.Route{
		{Destination: cidr(t, "10.200.200.1/32")}, // self route -> dropped
		{Destination: cidr(t, "1.2.3.4/32")},
		{Destination: cidr(t, "192.168.0.0/16")},
	}
	skip := map[string]bool{"10.200.200.1/32": true}

	out := filterOutDests(routes, skip)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(out), out)
	}
	for _, r := range out {
		if r.Destination.String() == "10.200.200.1/32" {
			t.Fatalf("self route was not filtered: %+v", out)
		}
	}
	// Empty skip set is a no-op passthrough.
	if got := filterOutDests(routes, nil); len(got) != len(routes) {
		t.Fatalf("nil skip should pass through: len = %d, want %d", len(got), len(routes))
	}
}

func TestRouteEventLogRecordPlanSkipsSelfRoutes(t *testing.T) {
	var log routeEventLog
	plan := &route.Plan{
		ToRemove: []route.Route{{Destination: cidr(t, "10.200.200.1/32")}}, // self route
		ToAdd: []route.Route{
			{Destination: cidr(t, "1.2.3.4/32"), Comment: "autoroute:a"},
			{Destination: cidr(t, "192.168.0.0/16")},
		},
	}
	auto := map[string]bool{"1.2.3.4/32": true}
	cfgSet := map[string]bool{"192.168.0.0/16": true}
	skip := map[string]bool{"10.200.200.1/32": true}

	log.recordPlan(plan, auto, cfgSet, skip)

	evs := log.snapshot()
	if len(evs) != 2 {
		t.Fatalf("len = %d, want 2 (self route skipped): %+v", len(evs), evs)
	}
	bySrc := map[string]string{}
	for _, e := range evs {
		bySrc[e.Destination] = e.Source
	}
	if bySrc["1.2.3.4/32"] != "auto" {
		t.Errorf("1.2.3.4/32 source = %q, want auto", bySrc["1.2.3.4/32"])
	}
	if bySrc["192.168.0.0/16"] != "config" {
		t.Errorf("192.168.0.0/16 source = %q, want config", bySrc["192.168.0.0/16"])
	}
	if _, ok := bySrc["10.200.200.1/32"]; ok {
		t.Errorf("self route should have been skipped")
	}
}
