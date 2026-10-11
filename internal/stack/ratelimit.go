package stack

import (
	"sync"
	"time"
)

// logRateLimiter bounds how often a repeated log message is emitted and
// counts suppressed occurrences so they can be reported with the next
// emitted message.
type logRateLimiter struct {
	mu         sync.Mutex
	interval   time.Duration
	last       time.Time
	suppressed int
}

// newLogRateLimiter returns a limiter that allows one log per interval.
func newLogRateLimiter(interval time.Duration) *logRateLimiter {
	return &logRateLimiter{interval: interval}
}

// allow reports whether an event may be logged now. When it returns true,
// the first return value carries the number of events suppressed since the
// last allowed log.
func (l *logRateLimiter) allow() (int, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	if now.Sub(l.last) >= l.interval {
		suppressed := l.suppressed
		l.suppressed = 0
		l.last = now
		return suppressed, true
	}
	l.suppressed++
	return 0, false
}
