package proxy

import (
	"context"
	"sync"
	"time"

	"github.com/postalsys/mutiauk/internal/socks5"
	"go.uber.org/zap"
)

// RelayPool manages a pool of UDP relay sessions with TTL-based cleanup.
type RelayPool struct {
	mu       sync.Mutex
	entries  map[string]*relayEntry
	ttl      time.Duration
	logger   *zap.Logger
	stopOnce sync.Once
	stopCh   chan struct{}
}

// relayEntry holds a relay and its last access time.
type relayEntry struct {
	relay    *socks5.UDPRelay
	lastUsed time.Time
}

// NewRelayPool creates a new relay pool with the given TTL.
func NewRelayPool(ttl time.Duration, logger *zap.Logger) *RelayPool {
	p := &RelayPool{
		entries: make(map[string]*relayEntry),
		ttl:     ttl,
		logger:  logger,
		stopCh:  make(chan struct{}),
	}
	go p.cleanupLoop()
	return p
}

// GetOrCreate returns an existing relay for the key, or creates a new one.
// The create callback runs outside the lock to avoid blocking other requests
// during potentially slow network I/O (SOCKS5 UDP ASSOCIATE handshake).
func (p *RelayPool) GetOrCreate(ctx context.Context, key string, create func(context.Context) (*socks5.UDPRelay, error)) (*socks5.UDPRelay, error) {
	// Fast path: check if relay already exists
	p.mu.Lock()
	if entry, ok := p.entries[key]; ok {
		entry.lastUsed = time.Now()
		p.mu.Unlock()
		return entry.relay, nil
	}
	p.mu.Unlock()

	// Slow path: create relay outside the lock (network I/O)
	relay, err := create(ctx)
	if err != nil {
		return nil, err
	}

	// Re-acquire lock and check again (another goroutine may have created it)
	p.mu.Lock()
	defer p.mu.Unlock()

	if entry, ok := p.entries[key]; ok {
		// Another goroutine created it while we were doing I/O; close ours
		relay.Close()
		entry.lastUsed = time.Now()
		return entry.relay, nil
	}

	p.entries[key] = &relayEntry{
		relay:    relay,
		lastUsed: time.Now(),
	}

	return relay, nil
}

// Clear closes all relays and clears the pool.
func (p *RelayPool) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, entry := range p.entries {
		if entry.relay != nil {
			entry.relay.Close()
		}
	}
	p.entries = make(map[string]*relayEntry)
}

// Close stops the cleanup goroutine and clears all relays.
func (p *RelayPool) Close() {
	p.stopOnce.Do(func() {
		close(p.stopCh)
	})
	p.Clear()
}

// cleanupLoop periodically removes stale relay entries.
func (p *RelayPool) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.cleanup()
		}
	}
}

// cleanup removes entries that have exceeded the TTL.
func (p *RelayPool) cleanup() {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	for key, entry := range p.entries {
		if now.Sub(entry.lastUsed) > p.ttl {
			if entry.relay != nil {
				entry.relay.Close()
			}
			delete(p.entries, key)
			if p.logger != nil {
				p.logger.Debug("cleaned up stale UDP relay", zap.String("key", key))
			}
		}
	}
}
