package daemon

import (
	"strings"
	"sync"
	"time"

	"github.com/Muti-Metroo/Mutiauk/internal/api"
	"github.com/Muti-Metroo/Mutiauk/internal/route"
)

// routeEventCapacity bounds the in-memory route-change log. The log is a
// diagnostic feed for the admin dashboard, not durable history, so a
// bounded ring is enough; oldest entries are dropped once full.
const routeEventCapacity = 250

// routeEventLog is a fixed-capacity, concurrency-safe ring buffer of
// route-change events. Each event carries a monotonic Seq so a poller
// can render only what it has not seen yet.
type routeEventLog struct {
	mu     sync.Mutex
	events []api.RouteEvent
	seq    uint64
}

// record appends one event with the next sequence number, evicting the
// oldest entry when the ring is full. Consecutive duplicates (same
// action+destination as the most recent entry) are suppressed: route
// reconciliation can re-detect the same unremovable kernel route on
// every poll, and a flood of identical lines would bury real changes.
func (l *routeEventLog) record(action, destination, source, comment string) {
	l.recordFull(action, destination, source, comment, "")
}

func (l *routeEventLog) recordFull(action, destination, source, comment, errText string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n := len(l.events); n > 0 {
		if last := l.events[n-1]; last.Action == action && last.Destination == destination {
			return
		}
	}
	l.seq++
	ev := api.RouteEvent{
		Seq:         l.seq,
		Time:        time.Now(),
		Action:      action,
		Destination: destination,
		Source:      source,
		Comment:     comment,
		Error:       errText,
	}
	if len(l.events) >= routeEventCapacity {
		l.events = append(l.events[1:], ev)
		return
	}
	l.events = append(l.events, ev)
}

// snapshot returns a copy of the buffered events, oldest first.
func (l *routeEventLog) snapshot() []api.RouteEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]api.RouteEvent, len(l.events))
	copy(out, l.events)
	return out
}

// recordResult logs the outcome of an applied route plan: "added"/"removed"
// for routes the kernel actually changed, and "failed-add"/"failed-remove"
// (with the cause in Error) for routes Apply could not install or withdraw.
// Recording after Apply - not from the plan - keeps the feed truthful: a
// failed apply no longer shows up as a successful change. autoSet/configSet
// classify each destination's source for display; destinations in skip (e.g.
// the interface's own address route, which the kernel manages and Diff
// perpetually flags) are not logged: they are not mutiauk routing changes.
func (l *routeEventLog) recordResult(res *route.ApplyResult, autoSet, configSet, skip map[string]bool) {
	if res == nil {
		return
	}
	for _, r := range res.Removed {
		dest := destString(r)
		if skip[dest] {
			continue
		}
		l.record("removed", dest, classifyRoute(dest, r.Comment, autoSet, configSet), r.Comment)
	}
	for _, r := range res.Added {
		dest := destString(r)
		if skip[dest] {
			continue
		}
		l.record("added", dest, classifyRoute(dest, r.Comment, autoSet, configSet), r.Comment)
	}
	for _, f := range res.Failed {
		dest := destString(f.Route)
		if skip[dest] {
			continue
		}
		action := "failed-add"
		if f.Removal {
			action = "failed-remove"
		}
		l.recordFull(action, dest, classifyRoute(dest, f.Route.Comment, autoSet, configSet), f.Route.Comment, f.Err.Error())
	}
}

func destString(r route.Route) string {
	if r.Destination == nil {
		return ""
	}
	return r.Destination.String()
}

// classifyRoute derives a display source for a route, preferring the
// live desired-set membership and falling back to the autoroute comment
// marker.
func classifyRoute(dest, comment string, autoSet, configSet map[string]bool) string {
	switch {
	case autoSet[dest]:
		return "auto"
	case configSet[dest]:
		return "config"
	case strings.HasPrefix(comment, "autoroute:"):
		return "auto"
	default:
		return "manual"
	}
}

// destSet builds a destination-string lookup set from a route slice.
func destSet(routes []route.Route) map[string]bool {
	set := make(map[string]bool, len(routes))
	for _, r := range routes {
		if r.Destination != nil {
			set[r.Destination.String()] = true
		}
	}
	return set
}
