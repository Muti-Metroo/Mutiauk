package nat

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// GCConfig holds garbage collection configuration
type GCConfig struct {
	Interval time.Duration
	Logger   *zap.Logger
}

// StartGC starts the garbage collection goroutine
func (t *Table) StartGC(ctx context.Context, cfg GCConfig) {
	if cfg.Interval == 0 {
		cfg.Interval = time.Minute
	}

	go t.gcLoop(ctx, cfg)
}

// gcLoop runs the garbage collection loop
func (t *Table) gcLoop(ctx context.Context, cfg GCConfig) {
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.gc(cfg.Logger)
		}
	}
}

// gc removes expired entries
func (t *Table) gc(logger *zap.Logger) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	var expired []string
	var toClose []*Entry

	for key, entry := range t.entries {
		entry.mu.Lock()
		isExpired := now.Sub(entry.LastSeen) > entry.TTL
		isClosed := entry.State == ConnStateClosed
		inUse := entry.refs > 0
		entry.mu.Unlock()

		// An entry whose handler is still active (refs > 0) is never
		// expired: LastSeen only advances on lookups, so a busy long-lived
		// connection would otherwise be torn down mid-stream once it
		// outlives its TTL. The handler's deferred Remove releases the
		// reference and deletes the entry when the copy loop exits.
		if isClosed || (isExpired && !inUse) {
			expired = append(expired, key)
			toClose = append(toClose, entry)
		}
	}

	for _, key := range expired {
		delete(t.entries, key)
	}

	// Close entries outside the lock
	go func() {
		for _, entry := range toClose {
			entry.Close()
		}
	}()

	if len(expired) > 0 && logger != nil {
		logger.Debug("NAT GC completed",
			zap.Int("expired", len(expired)),
			zap.Int("remaining", len(t.entries)),
		)
	}
}

// GCNow triggers an immediate garbage collection
func (t *Table) GCNow(logger *zap.Logger) {
	t.gc(logger)
}
