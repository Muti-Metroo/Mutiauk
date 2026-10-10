package nat

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// --- ConnState Tests ---

func TestConnStateString(t *testing.T) {
	tests := []struct {
		state ConnState
		want  string
	}{
		{ConnStateNew, "NEW"},
		{ConnStateEstablished, "ESTABLISHED"},
		{ConnStateClosing, "CLOSING"},
		{ConnStateClosed, "CLOSED"},
		{ConnState(99), "UNKNOWN(99)"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.state.String()
			if got != tt.want {
				t.Errorf("String() = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- Entry Tests ---

func TestEntryTouch(t *testing.T) {
	entry := &Entry{
		LastSeen: time.Now().Add(-time.Hour),
	}

	before := entry.LastSeen
	time.Sleep(10 * time.Millisecond)
	entry.Touch()

	if !entry.LastSeen.After(before) {
		t.Error("Touch() did not update LastSeen")
	}
}

func TestEntryState(t *testing.T) {
	entry := &Entry{
		State: ConnStateNew,
	}

	if entry.GetState() != ConnStateNew {
		t.Errorf("GetState() = %v, want NEW", entry.GetState())
	}

	entry.SetState(ConnStateEstablished)
	if entry.GetState() != ConnStateEstablished {
		t.Errorf("GetState() = %v, want ESTABLISHED", entry.GetState())
	}

	entry.SetState(ConnStateClosed)
	if entry.GetState() != ConnStateClosed {
		t.Errorf("GetState() = %v, want CLOSED", entry.GetState())
	}
}

func TestEntryIsExpired(t *testing.T) {
	t.Run("not expired", func(t *testing.T) {
		entry := &Entry{
			LastSeen: time.Now(),
			TTL:      time.Hour,
		}
		if entry.IsExpired() {
			t.Error("IsExpired() = true, want false")
		}
	})

	t.Run("expired", func(t *testing.T) {
		entry := &Entry{
			LastSeen: time.Now().Add(-2 * time.Hour),
			TTL:      time.Hour,
		}
		if !entry.IsExpired() {
			t.Error("IsExpired() = false, want true")
		}
	})

	t.Run("just expired", func(t *testing.T) {
		entry := &Entry{
			LastSeen: time.Now().Add(-100 * time.Millisecond),
			TTL:      50 * time.Millisecond,
		}
		if !entry.IsExpired() {
			t.Error("IsExpired() = false, want true")
		}
	})
}

func TestEntryClose(t *testing.T) {
	// Create a mock connection using net.Pipe
	client, server := net.Pipe()
	defer server.Close()

	entry := &Entry{
		ProxyConn: client,
		State:     ConnStateEstablished,
	}

	err := entry.Close()
	if err != nil {
		t.Errorf("Close() error = %v", err)
	}

	if entry.GetState() != ConnStateClosed {
		t.Errorf("State = %v, want CLOSED", entry.GetState())
	}

	// Verify connection is closed
	_, err = client.Write([]byte("test"))
	if err == nil {
		t.Error("expected error writing to closed connection")
	}
}

func TestEntryCloseNoConn(t *testing.T) {
	entry := &Entry{
		ProxyConn: nil,
		State:     ConnStateEstablished,
	}

	err := entry.Close()
	if err != nil {
		t.Errorf("Close() error = %v", err)
	}

	if entry.GetState() != ConnStateClosed {
		t.Errorf("State = %v, want CLOSED", entry.GetState())
	}
}

func TestEntryKey(t *testing.T) {
	entry := &Entry{
		Protocol: "tcp",
		SrcAddr:  netip.MustParseAddrPort("192.168.1.1:12345"),
		DstAddr:  netip.MustParseAddrPort("10.0.0.1:80"),
	}

	key := entry.Key()
	expected := "tcp:192.168.1.1:12345->10.0.0.1:80"
	if key != expected {
		t.Errorf("Key() = %v, want %v", key, expected)
	}
}

// --- MakeKey Tests ---

func TestMakeKey(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		src      netip.AddrPort
		dst      netip.AddrPort
		want     string
	}{
		{
			name:     "TCP IPv4",
			protocol: "tcp",
			src:      netip.MustParseAddrPort("192.168.1.1:12345"),
			dst:      netip.MustParseAddrPort("10.0.0.1:80"),
			want:     "tcp:192.168.1.1:12345->10.0.0.1:80",
		},
		{
			name:     "UDP IPv4",
			protocol: "udp",
			src:      netip.MustParseAddrPort("172.16.0.1:5353"),
			dst:      netip.MustParseAddrPort("8.8.8.8:53"),
			want:     "udp:172.16.0.1:5353->8.8.8.8:53",
		},
		{
			name:     "TCP IPv6",
			protocol: "tcp",
			src:      netip.MustParseAddrPort("[::1]:12345"),
			dst:      netip.MustParseAddrPort("[2001:db8::1]:443"),
			want:     "tcp:[::1]:12345->[2001:db8::1]:443",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MakeKey(tt.protocol, tt.src, tt.dst)
			if got != tt.want {
				t.Errorf("MakeKey() = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- DefaultConfig Tests ---

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.MaxEntries != 65536 {
		t.Errorf("MaxEntries = %v, want 65536", cfg.MaxEntries)
	}
	if cfg.TCPTTL != time.Hour {
		t.Errorf("TCPTTL = %v, want 1h", cfg.TCPTTL)
	}
	if cfg.UDPTTL != 5*time.Minute {
		t.Errorf("UDPTTL = %v, want 5m", cfg.UDPTTL)
	}
}

// --- Table Tests ---

func TestNewTable(t *testing.T) {
	t.Run("with config", func(t *testing.T) {
		cfg := Config{
			MaxEntries: 1000,
			TCPTTL:     30 * time.Minute,
			UDPTTL:     2 * time.Minute,
		}

		table := NewTable(cfg)
		if table.maxEntries != 1000 {
			t.Errorf("maxEntries = %v, want 1000", table.maxEntries)
		}
		if table.tcpTTL != 30*time.Minute {
			t.Errorf("tcpTTL = %v, want 30m", table.tcpTTL)
		}
		if table.udpTTL != 2*time.Minute {
			t.Errorf("udpTTL = %v, want 2m", table.udpTTL)
		}
	})

	t.Run("with defaults", func(t *testing.T) {
		table := NewTable(Config{})
		if table.maxEntries != 65536 {
			t.Errorf("maxEntries = %v, want 65536", table.maxEntries)
		}
		if table.tcpTTL != time.Hour {
			t.Errorf("tcpTTL = %v, want 1h", table.tcpTTL)
		}
		if table.udpTTL != 5*time.Minute {
			t.Errorf("udpTTL = %v, want 5m", table.udpTTL)
		}
	})
}

func TestTableInsertAndLookup(t *testing.T) {
	table := NewTable(DefaultConfig())

	src := netip.MustParseAddrPort("192.168.1.1:12345")
	dst := netip.MustParseAddrPort("10.0.0.1:80")

	entry := &Entry{
		Protocol: "tcp",
		SrcAddr:  src,
		DstAddr:  dst,
	}

	// Insert
	err := table.Insert(entry)
	if err != nil {
		t.Fatalf("Insert() error = %v", err)
	}

	// Lookup
	found, ok := table.Lookup("tcp", src, dst)
	if !ok {
		t.Fatal("Lookup() returned false")
	}
	if found.Protocol != "tcp" {
		t.Errorf("Protocol = %v, want tcp", found.Protocol)
	}

	// Verify defaults were set
	if entry.TTL != time.Hour {
		t.Errorf("TTL = %v, want 1h", entry.TTL)
	}
	if entry.State != ConnStateNew {
		t.Errorf("State = %v, want NEW", entry.State)
	}
}

func TestTableLookupNotFound(t *testing.T) {
	table := NewTable(DefaultConfig())

	src := netip.MustParseAddrPort("192.168.1.1:12345")
	dst := netip.MustParseAddrPort("10.0.0.1:80")

	_, ok := table.Lookup("tcp", src, dst)
	if ok {
		t.Error("Lookup() returned true for non-existent entry")
	}
}

func TestTableLookupByKey(t *testing.T) {
	table := NewTable(DefaultConfig())

	src := netip.MustParseAddrPort("192.168.1.1:12345")
	dst := netip.MustParseAddrPort("10.0.0.1:80")

	entry := &Entry{
		Protocol: "tcp",
		SrcAddr:  src,
		DstAddr:  dst,
	}

	table.Insert(entry)

	key := "tcp:192.168.1.1:12345->10.0.0.1:80"
	found, ok := table.LookupByKey(key)
	if !ok {
		t.Fatal("LookupByKey() returned false")
	}
	if found != entry {
		t.Error("LookupByKey() returned wrong entry")
	}
}

func TestTableLookupClosedEntry(t *testing.T) {
	table := NewTable(DefaultConfig())

	src := netip.MustParseAddrPort("192.168.1.1:12345")
	dst := netip.MustParseAddrPort("10.0.0.1:80")

	entry := &Entry{
		Protocol: "tcp",
		SrcAddr:  src,
		DstAddr:  dst,
	}

	table.Insert(entry)
	entry.SetState(ConnStateClosed)

	// Closed entries should not be found
	_, ok := table.Lookup("tcp", src, dst)
	if ok {
		t.Error("Lookup() returned true for closed entry")
	}
}

func TestTableInsertDuplicate(t *testing.T) {
	table := NewTable(DefaultConfig())

	src := netip.MustParseAddrPort("192.168.1.1:12345")
	dst := netip.MustParseAddrPort("10.0.0.1:80")

	entry := &Entry{
		Protocol: "tcp",
		SrcAddr:  src,
		DstAddr:  dst,
	}

	table.Insert(entry)

	// Try to insert duplicate
	err := table.Insert(&Entry{
		Protocol: "tcp",
		SrcAddr:  src,
		DstAddr:  dst,
	})

	if err == nil {
		t.Error("expected error for duplicate entry")
	}
}

func TestTableInsertFull(t *testing.T) {
	table := NewTable(Config{MaxEntries: 2})

	// Insert 2 entries
	for i := 0; i < 2; i++ {
		err := table.Insert(&Entry{
			Protocol: "tcp",
			SrcAddr:  netip.MustParseAddrPort("192.168.1.1:" + string(rune('0'+i))),
			DstAddr:  netip.MustParseAddrPort("10.0.0.1:80"),
		})
		if err != nil {
			t.Fatalf("Insert() error = %v", err)
		}
	}

	// Third insert should fail
	err := table.Insert(&Entry{
		Protocol: "tcp",
		SrcAddr:  netip.MustParseAddrPort("192.168.1.1:9999"),
		DstAddr:  netip.MustParseAddrPort("10.0.0.1:80"),
	})

	if err == nil {
		t.Error("expected error for full table")
	}
}

func TestTableInsertTTL(t *testing.T) {
	table := NewTable(Config{
		TCPTTL: 30 * time.Minute,
		UDPTTL: 2 * time.Minute,
	})

	t.Run("TCP gets TCP TTL", func(t *testing.T) {
		entry := &Entry{
			Protocol: "tcp",
			SrcAddr:  netip.MustParseAddrPort("192.168.1.1:1000"),
			DstAddr:  netip.MustParseAddrPort("10.0.0.1:80"),
		}
		table.Insert(entry)
		if entry.TTL != 30*time.Minute {
			t.Errorf("TTL = %v, want 30m", entry.TTL)
		}
	})

	t.Run("UDP gets UDP TTL", func(t *testing.T) {
		entry := &Entry{
			Protocol: "udp",
			SrcAddr:  netip.MustParseAddrPort("192.168.1.1:2000"),
			DstAddr:  netip.MustParseAddrPort("8.8.8.8:53"),
		}
		table.Insert(entry)
		if entry.TTL != 2*time.Minute {
			t.Errorf("TTL = %v, want 2m", entry.TTL)
		}
	})

	t.Run("custom TTL preserved", func(t *testing.T) {
		entry := &Entry{
			Protocol: "tcp",
			SrcAddr:  netip.MustParseAddrPort("192.168.1.1:3000"),
			DstAddr:  netip.MustParseAddrPort("10.0.0.1:80"),
			TTL:      5 * time.Minute,
		}
		table.Insert(entry)
		if entry.TTL != 5*time.Minute {
			t.Errorf("TTL = %v, want 5m", entry.TTL)
		}
	})
}

func TestTableRemove(t *testing.T) {
	table := NewTable(DefaultConfig())

	src := netip.MustParseAddrPort("192.168.1.1:12345")
	dst := netip.MustParseAddrPort("10.0.0.1:80")

	entry := &Entry{
		Protocol: "tcp",
		SrcAddr:  src,
		DstAddr:  dst,
	}

	table.Insert(entry)
	if table.Size() != 1 {
		t.Fatalf("Size() = %v, want 1", table.Size())
	}

	table.Remove("tcp", src, dst)

	if table.Size() != 0 {
		t.Errorf("Size() = %v, want 0", table.Size())
	}

	// Verify entry is closed
	if entry.GetState() != ConnStateClosed {
		t.Errorf("State = %v, want CLOSED", entry.GetState())
	}
}

func TestTableRemoveByKey(t *testing.T) {
	table := NewTable(DefaultConfig())

	entry := &Entry{
		Protocol: "tcp",
		SrcAddr:  netip.MustParseAddrPort("192.168.1.1:12345"),
		DstAddr:  netip.MustParseAddrPort("10.0.0.1:80"),
	}

	table.Insert(entry)

	key := entry.Key()
	table.RemoveByKey(key)

	if table.Size() != 0 {
		t.Errorf("Size() = %v, want 0", table.Size())
	}
}

func TestTableRemoveNonExistent(t *testing.T) {
	table := NewTable(DefaultConfig())

	// Should not panic
	table.Remove("tcp",
		netip.MustParseAddrPort("192.168.1.1:12345"),
		netip.MustParseAddrPort("10.0.0.1:80"),
	)

	table.RemoveByKey("nonexistent")
}

func TestTableSize(t *testing.T) {
	table := NewTable(DefaultConfig())

	if table.Size() != 0 {
		t.Errorf("Size() = %v, want 0", table.Size())
	}

	for i := 0; i < 5; i++ {
		table.Insert(&Entry{
			Protocol: "tcp",
			SrcAddr:  netip.MustParseAddrPort("192.168.1.1:" + string(rune('0'+i))),
			DstAddr:  netip.MustParseAddrPort("10.0.0.1:80"),
		})
	}

	if table.Size() != 5 {
		t.Errorf("Size() = %v, want 5", table.Size())
	}
}

func TestTableList(t *testing.T) {
	table := NewTable(DefaultConfig())

	entries := []*Entry{
		{
			Protocol: "tcp",
			SrcAddr:  netip.MustParseAddrPort("192.168.1.1:1000"),
			DstAddr:  netip.MustParseAddrPort("10.0.0.1:80"),
		},
		{
			Protocol: "udp",
			SrcAddr:  netip.MustParseAddrPort("192.168.1.1:2000"),
			DstAddr:  netip.MustParseAddrPort("8.8.8.8:53"),
		},
	}

	for _, e := range entries {
		table.Insert(e)
	}

	list := table.List()
	if len(list) != 2 {
		t.Errorf("len(List()) = %v, want 2", len(list))
	}
}

func TestTableClear(t *testing.T) {
	table := NewTable(DefaultConfig())

	for i := 0; i < 5; i++ {
		table.Insert(&Entry{
			Protocol: "tcp",
			SrcAddr:  netip.MustParseAddrPort("192.168.1.1:" + string(rune('0'+i))),
			DstAddr:  netip.MustParseAddrPort("10.0.0.1:80"),
		})
	}

	if table.Size() != 5 {
		t.Fatalf("Size() = %v, want 5", table.Size())
	}

	table.Clear()

	if table.Size() != 0 {
		t.Errorf("Size() = %v, want 0 after Clear()", table.Size())
	}
}

func TestTableStats(t *testing.T) {
	table := NewTable(DefaultConfig())

	// Add TCP entries
	for i := 0; i < 3; i++ {
		entry := &Entry{
			Protocol: "tcp",
			SrcAddr:  netip.MustParseAddrPort("192.168.1.1:" + string(rune('0'+i))),
			DstAddr:  netip.MustParseAddrPort("10.0.0.1:80"),
		}
		table.Insert(entry)
		if i == 0 {
			entry.SetState(ConnStateEstablished)
		} else if i == 1 {
			entry.SetState(ConnStateClosing)
		}
		// i == 2 stays as NEW
	}

	// Add UDP entries
	for i := 0; i < 2; i++ {
		table.Insert(&Entry{
			Protocol: "udp",
			SrcAddr:  netip.MustParseAddrPort("192.168.1.1:" + string(rune('5'+i))),
			DstAddr:  netip.MustParseAddrPort("8.8.8.8:53"),
		})
	}

	stats := table.Stats()

	if stats.TotalEntries != 5 {
		t.Errorf("TotalEntries = %v, want 5", stats.TotalEntries)
	}
	if stats.TCPEntries != 3 {
		t.Errorf("TCPEntries = %v, want 3", stats.TCPEntries)
	}
	if stats.UDPEntries != 2 {
		t.Errorf("UDPEntries = %v, want 2", stats.UDPEntries)
	}
	if stats.NewEntries != 3 { // 1 TCP + 2 UDP
		t.Errorf("NewEntries = %v, want 3", stats.NewEntries)
	}
	if stats.Established != 1 {
		t.Errorf("Established = %v, want 1", stats.Established)
	}
	if stats.Closing != 1 {
		t.Errorf("Closing = %v, want 1", stats.Closing)
	}
}

// --- Concurrency Tests ---

func TestTableConcurrency(t *testing.T) {
	table := NewTable(Config{MaxEntries: 10000})

	var wg sync.WaitGroup
	const numGoroutines = 10
	const opsPerGoroutine = 100

	// Concurrent inserts
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				port := 10000 + g*1000 + i
				table.Insert(&Entry{
					Protocol: "tcp",
					SrcAddr:  netip.MustParseAddrPort(fmt.Sprintf("192.168.1.1:%d", port)),
					DstAddr:  netip.MustParseAddrPort("10.0.0.1:80"),
				})
			}
		}(g)
	}

	// Concurrent lookups
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				port := 10000 + g*1000 + i
				table.Lookup("tcp",
					netip.MustParseAddrPort(fmt.Sprintf("192.168.1.1:%d", port)),
					netip.MustParseAddrPort("10.0.0.1:80"),
				)
			}
		}(g)
	}

	// Concurrent stats
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				table.Stats()
				table.Size()
			}
		}()
	}

	wg.Wait()
}

// --- GC Tests ---

func TestGCNow(t *testing.T) {
	table := NewTable(Config{
		TCPTTL: 50 * time.Millisecond,
	})

	// Insert an entry
	entry := &Entry{
		Protocol: "tcp",
		SrcAddr:  netip.MustParseAddrPort("192.168.1.1:12345"),
		DstAddr:  netip.MustParseAddrPort("10.0.0.1:80"),
	}
	table.Insert(entry)

	if table.Size() != 1 {
		t.Fatalf("Size() = %v, want 1", table.Size())
	}

	// Wait for entry to expire
	time.Sleep(100 * time.Millisecond)

	// Run GC
	table.GCNow(zap.NewNop())

	// Entry should be removed
	if table.Size() != 0 {
		t.Errorf("Size() = %v, want 0 after GC", table.Size())
	}
}

func TestGCClosedEntries(t *testing.T) {
	table := NewTable(Config{
		TCPTTL: time.Hour, // Long TTL
	})

	entry := &Entry{
		Protocol: "tcp",
		SrcAddr:  netip.MustParseAddrPort("192.168.1.1:12345"),
		DstAddr:  netip.MustParseAddrPort("10.0.0.1:80"),
	}
	table.Insert(entry)

	// Close the entry
	entry.SetState(ConnStateClosed)

	// Run GC
	table.GCNow(nil)

	// Closed entry should be removed even if not expired
	if table.Size() != 0 {
		t.Errorf("Size() = %v, want 0 after GC", table.Size())
	}
}

func TestStartGC(t *testing.T) {
	table := NewTable(Config{
		TCPTTL: 50 * time.Millisecond,
	})

	// Insert an entry
	table.Insert(&Entry{
		Protocol: "tcp",
		SrcAddr:  netip.MustParseAddrPort("192.168.1.1:12345"),
		DstAddr:  netip.MustParseAddrPort("10.0.0.1:80"),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start GC with short interval
	table.StartGC(ctx, GCConfig{
		Interval: 50 * time.Millisecond,
		Logger:   zap.NewNop(),
	})

	// Wait for entry to expire and GC to run
	time.Sleep(200 * time.Millisecond)

	// Entry should be removed
	if table.Size() != 0 {
		t.Errorf("Size() = %v, want 0 after GC", table.Size())
	}
}

func TestStartGCContextCancel(t *testing.T) {
	table := NewTable(DefaultConfig())

	ctx, cancel := context.WithCancel(context.Background())

	// Start GC
	table.StartGC(ctx, GCConfig{
		Interval: time.Second,
	})

	// Cancel immediately
	cancel()

	// Give GC goroutine time to exit
	time.Sleep(50 * time.Millisecond)

	// Should not panic or hang
}

func TestStartGCDefaultInterval(t *testing.T) {
	table := NewTable(DefaultConfig())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start with zero interval (should use default)
	table.StartGC(ctx, GCConfig{
		Interval: 0,
	})

	// Should not panic
}
