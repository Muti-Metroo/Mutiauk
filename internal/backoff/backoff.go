// Package backoff holds reconnect-backoff helpers shared by the
// long-lived streams to the Muti Metroo agent (route-watch, expose).
package backoff

import (
	"math/rand"
	"time"
)

// Jitter returns d +/- 25%, with a 100ms floor, to spread reconnects across
// multiple clients sharing the same agent.
func Jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	half := int64(d / 4)
	delta := rand.Int63n(2*half+1) - half
	out := time.Duration(int64(d) + delta)
	if out < 100*time.Millisecond {
		out = 100 * time.Millisecond
	}
	return out
}
