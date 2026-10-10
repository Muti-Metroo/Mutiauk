package nat

import (
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"
)

// ConnState represents the state of a connection
type ConnState int

const (
	ConnStateNew ConnState = iota
	ConnStateEstablished
	ConnStateClosing
	ConnStateClosed
)

func (s ConnState) String() string {
	switch s {
	case ConnStateNew:
		return "NEW"
	case ConnStateEstablished:
		return "ESTABLISHED"
	case ConnStateClosing:
		return "CLOSING"
	case ConnStateClosed:
		return "CLOSED"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", s)
	}
}

// Entry represents a NAT mapping
type Entry struct {
	Protocol  string         // "tcp" or "udp"
	SrcAddr   netip.AddrPort // Original source
	DstAddr   netip.AddrPort // Original destination
	ProxyConn net.Conn       // SOCKS5 connection (for TCP)
	UDPRelay  interface{}    // UDP relay (type will be *socks5.UDPRelay)
	Created   time.Time
	LastSeen  time.Time
	State     ConnState
	TTL       time.Duration

	mu        sync.Mutex // Protects State and LastSeen
	closeOnce sync.Once  // Guards Close() against double-call
}

// Touch updates the last seen time
func (e *Entry) Touch() {
	e.mu.Lock()
	e.LastSeen = time.Now()
	e.mu.Unlock()
}

// SetState updates the connection state
func (e *Entry) SetState(state ConnState) {
	e.mu.Lock()
	e.State = state
	e.LastSeen = time.Now()
	e.mu.Unlock()
}

// GetState returns the current state
func (e *Entry) GetState() ConnState {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.State
}

// IsExpired checks if the entry has expired
func (e *Entry) IsExpired() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return time.Since(e.LastSeen) > e.TTL
}

// Close closes the entry's connections. Safe to call multiple times.
func (e *Entry) Close() error {
	var err error
	e.closeOnce.Do(func() {
		e.SetState(ConnStateClosed)
		if e.ProxyConn != nil {
			err = e.ProxyConn.Close()
		}
	})
	return err
}

// Key generates a unique key for this entry
func (e *Entry) Key() string {
	return MakeKey(e.Protocol, e.SrcAddr, e.DstAddr)
}

// MakeKey creates a key from components
func MakeKey(protocol string, src, dst netip.AddrPort) string {
	return fmt.Sprintf("%s:%s->%s", protocol, src.String(), dst.String())
}

// Table manages NAT entries
type Table struct {
	mu         sync.RWMutex
	entries    map[string]*Entry
	maxEntries int
	tcpTTL     time.Duration
	udpTTL     time.Duration
}

// Config holds NAT table configuration
type Config struct {
	MaxEntries int
	TCPTTL     time.Duration
	UDPTTL     time.Duration
}

// DefaultConfig returns default NAT table configuration
func DefaultConfig() Config {
	return Config{
		MaxEntries: 65536,
		TCPTTL:     time.Hour,
		UDPTTL:     5 * time.Minute,
	}
}

// NewTable creates a new NAT table
func NewTable(cfg Config) *Table {
	if cfg.MaxEntries == 0 {
		cfg.MaxEntries = 65536
	}
	if cfg.TCPTTL == 0 {
		cfg.TCPTTL = time.Hour
	}
	if cfg.UDPTTL == 0 {
		cfg.UDPTTL = 5 * time.Minute
	}

	return &Table{
		entries:    make(map[string]*Entry),
		maxEntries: cfg.MaxEntries,
		tcpTTL:     cfg.TCPTTL,
		udpTTL:     cfg.UDPTTL,
	}
}

// Lookup finds an existing NAT entry
func (t *Table) Lookup(protocol string, src, dst netip.AddrPort) (*Entry, bool) {
	key := MakeKey(protocol, src, dst)

	t.mu.RLock()
	entry, ok := t.entries[key]
	t.mu.RUnlock()

	if ok && entry.GetState() != ConnStateClosed {
		entry.Touch()
		return entry, true
	}

	return nil, false
}

// LookupByKey finds an entry by its key
func (t *Table) LookupByKey(key string) (*Entry, bool) {
	t.mu.RLock()
	entry, ok := t.entries[key]
	t.mu.RUnlock()

	if ok && entry.GetState() != ConnStateClosed {
		entry.Touch()
		return entry, true
	}

	return nil, false
}

// Insert adds a new NAT entry
func (t *Table) Insert(entry *Entry) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.entries) >= t.maxEntries {
		return fmt.Errorf("NAT table full (%d entries)", t.maxEntries)
	}

	key := entry.Key()
	if _, exists := t.entries[key]; exists {
		return fmt.Errorf("entry already exists: %s", key)
	}

	// Set TTL based on protocol
	if entry.TTL == 0 {
		switch entry.Protocol {
		case "tcp":
			entry.TTL = t.tcpTTL
		case "udp":
			entry.TTL = t.udpTTL
		default:
			entry.TTL = t.udpTTL
		}
	}

	if entry.Created.IsZero() {
		entry.Created = time.Now()
	}
	if entry.LastSeen.IsZero() {
		entry.LastSeen = time.Now()
	}
	if entry.State == 0 {
		entry.State = ConnStateNew
	}

	t.entries[key] = entry
	return nil
}

// Remove removes a NAT entry
func (t *Table) Remove(protocol string, src, dst netip.AddrPort) {
	key := MakeKey(protocol, src, dst)

	t.mu.Lock()
	entry, ok := t.entries[key]
	if ok {
		delete(t.entries, key)
	}
	t.mu.Unlock()

	if ok && entry != nil {
		entry.Close()
	}
}

// RemoveByKey removes an entry by its key
func (t *Table) RemoveByKey(key string) {
	t.mu.Lock()
	entry, ok := t.entries[key]
	if ok {
		delete(t.entries, key)
	}
	t.mu.Unlock()

	if ok && entry != nil {
		entry.Close()
	}
}

// Size returns the number of entries
func (t *Table) Size() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.entries)
}

// List returns all entries
func (t *Table) List() []*Entry {
	t.mu.RLock()
	defer t.mu.RUnlock()

	entries := make([]*Entry, 0, len(t.entries))
	for _, e := range t.entries {
		entries = append(entries, e)
	}
	return entries
}

// Clear removes all entries
func (t *Table) Clear() {
	t.mu.Lock()
	entries := t.entries
	t.entries = make(map[string]*Entry)
	t.mu.Unlock()

	// Close all entries outside the lock
	for _, entry := range entries {
		entry.Close()
	}
}

// Stats returns table statistics
type Stats struct {
	TotalEntries int
	TCPEntries   int
	UDPEntries   int
	NewEntries   int
	Established  int
	Closing      int
}

// Stats returns table statistics
func (t *Table) Stats() Stats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var stats Stats
	stats.TotalEntries = len(t.entries)

	for _, e := range t.entries {
		switch e.Protocol {
		case "tcp":
			stats.TCPEntries++
		case "udp":
			stats.UDPEntries++
		}

		switch e.GetState() {
		case ConnStateNew:
			stats.NewEntries++
		case ConnStateEstablished:
			stats.Established++
		case ConnStateClosing:
			stats.Closing++
		}
	}

	return stats
}
