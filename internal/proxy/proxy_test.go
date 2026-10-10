package proxy

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/postalsys/mutiauk/internal/nat"
	"github.com/postalsys/mutiauk/internal/socks5"
	"go.uber.org/zap"
)

// --- TCPForwarder Tests ---

func TestNewTCPForwarder(t *testing.T) {
	logger := zap.NewNop()
	client := &socks5.Client{}
	natTable := nat.NewTable(nat.DefaultConfig())

	forwarder := NewTCPForwarder(client, natTable, logger)

	if forwarder == nil {
		t.Fatal("NewTCPForwarder returned nil")
	}
	if forwarder.clientHolder.Get() != client {
		t.Error("client not set correctly")
	}
	if forwarder.natTable != natTable {
		t.Error("natTable not set correctly")
	}
	if forwarder.bufferSize != DefaultTCPBufferSize {
		t.Errorf("bufferSize = %d, want %d", forwarder.bufferSize, DefaultTCPBufferSize)
	}
}

func TestTCPForwarder_UpdateClient(t *testing.T) {
	logger := zap.NewNop()
	client1 := &socks5.Client{}
	client2 := &socks5.Client{}
	natTable := nat.NewTable(nat.DefaultConfig())

	forwarder := NewTCPForwarder(client1, natTable, logger)

	if forwarder.clientHolder.Get() != client1 {
		t.Error("initial client not set correctly")
	}

	forwarder.UpdateClient(client2)

	if forwarder.clientHolder.Get() != client2 {
		t.Error("client not updated correctly")
	}
}

func TestTCPForwarder_UpdateClient_Concurrent(t *testing.T) {
	logger := zap.NewNop()
	client := &socks5.Client{}
	natTable := nat.NewTable(nat.DefaultConfig())
	forwarder := NewTCPForwarder(client, natTable, logger)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			newClient := &socks5.Client{}
			forwarder.UpdateClient(newClient)
		}()
	}

	// Also read concurrently
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = forwarder.clientHolder.Get()
		}()
	}

	wg.Wait()
}

func TestTCPForwarder_PendingConnections(t *testing.T) {
	logger := zap.NewNop()
	client := &socks5.Client{}
	natTable := nat.NewTable(nat.DefaultConfig())
	forwarder := NewTCPForwarder(client, natTable, logger)

	// Test that pendingConns map is initialized
	key := "192.168.1.1:12345->10.0.0.1:80"

	// Create a mock connection using net.Pipe
	conn1, conn2 := net.Pipe()
	defer conn1.Close()
	defer conn2.Close()

	// Store a pending connection
	forwarder.pendingConns.Store(key, conn1)

	// Verify it can be retrieved
	if val, ok := forwarder.pendingConns.Load(key); !ok {
		t.Error("pending connection not stored")
	} else if val.(net.Conn) != conn1 {
		t.Error("wrong connection retrieved")
	}

	// Test LoadAndDelete
	if val, ok := forwarder.pendingConns.LoadAndDelete(key); !ok {
		t.Error("LoadAndDelete failed")
	} else if val.(net.Conn) != conn1 {
		t.Error("wrong connection from LoadAndDelete")
	}

	// Verify it's deleted
	if _, ok := forwarder.pendingConns.Load(key); ok {
		t.Error("pending connection should be deleted after LoadAndDelete")
	}
}

// TestBidirectionalCopy tests the bidirectional copy function
func TestBidirectionalCopy(t *testing.T) {
	// Create two pipe pairs for local and remote
	localClient, localServer := net.Pipe()
	remoteClient, remoteServer := net.Pipe()

	defer localClient.Close()
	defer localServer.Close()
	defer remoteClient.Close()
	defer remoteServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Start relay in goroutine
	relayDone := make(chan error, 1)
	go func() {
		relayDone <- bidirectionalCopy(ctx, localServer, remoteClient, DefaultTCPBufferSize)
	}()

	// Test local -> remote
	testData := []byte("Hello from local")
	go func() {
		localClient.Write(testData)
	}()

	buf := make([]byte, len(testData))
	n, err := remoteServer.Read(buf)
	if err != nil {
		t.Errorf("failed to read from remote: %v", err)
	}
	if string(buf[:n]) != string(testData) {
		t.Errorf("data mismatch: got %q, want %q", buf[:n], testData)
	}

	// Test remote -> local
	responseData := []byte("Hello from remote")
	go func() {
		remoteServer.Write(responseData)
	}()

	buf = make([]byte, len(responseData))
	n, err = localClient.Read(buf)
	if err != nil {
		t.Errorf("failed to read from local: %v", err)
	}
	if string(buf[:n]) != string(responseData) {
		t.Errorf("data mismatch: got %q, want %q", buf[:n], responseData)
	}

	// Close one side to complete relay
	localClient.Close()

	// Wait for relay to complete
	select {
	case <-relayDone:
		// Expected
	case <-time.After(time.Second):
		t.Error("relay did not complete in time")
	}
}

func TestBidirectionalCopy_ContextCancel(t *testing.T) {
	localClient, localServer := net.Pipe()
	remoteClient, remoteServer := net.Pipe()

	defer localClient.Close()
	defer localServer.Close()
	defer remoteClient.Close()
	defer remoteServer.Close()

	ctx, cancel := context.WithCancel(context.Background())

	relayDone := make(chan error, 1)
	go func() {
		relayDone <- bidirectionalCopy(ctx, localServer, remoteClient, DefaultTCPBufferSize)
	}()

	// Give relay time to start
	time.Sleep(50 * time.Millisecond)

	// Cancel context
	cancel()

	// Wait for relay to return
	select {
	case err := <-relayDone:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Error("relay did not respond to context cancellation")
	}
}

// --- UDPForwarder Tests ---

func TestNewUDPForwarder(t *testing.T) {
	logger := zap.NewNop()
	client := &socks5.Client{}
	natTable := nat.NewTable(nat.DefaultConfig())

	forwarder := NewUDPForwarder(client, natTable, logger)

	if forwarder == nil {
		t.Fatal("NewUDPForwarder returned nil")
	}
	if forwarder.clientHolder.Get() != client {
		t.Error("client not set correctly")
	}
	if forwarder.natTable != natTable {
		t.Error("natTable not set correctly")
	}
	if forwarder.bufferSize != DefaultUDPBufferSize {
		t.Errorf("bufferSize = %d, want 65535", forwarder.bufferSize)
	}
	if forwarder.relayTTL() != 5*time.Minute {
		t.Errorf("relayTTL = %v, want 5m", forwarder.relayTTL())
	}
	if forwarder.relayPool == nil {
		t.Error("relayPool not initialized")
	}

	forwarder.Close()
}

func TestUDPForwarder_UpdateClient(t *testing.T) {
	logger := zap.NewNop()
	client1 := &socks5.Client{}
	client2 := &socks5.Client{}
	natTable := nat.NewTable(nat.DefaultConfig())

	forwarder := NewUDPForwarder(client1, natTable, logger)
	defer forwarder.Close()

	forwarder.UpdateClient(client2)

	if forwarder.clientHolder.Get() != client2 {
		t.Error("client not updated correctly")
	}
}

func TestUDPForwarder_Close(t *testing.T) {
	logger := zap.NewNop()
	client := &socks5.Client{}
	natTable := nat.NewTable(nat.DefaultConfig())

	forwarder := NewUDPForwarder(client, natTable, logger)

	// Close should not panic
	forwarder.Close()
}

func TestUDPForwarder_Close_Multiple(t *testing.T) {
	logger := zap.NewNop()
	client := &socks5.Client{}
	natTable := nat.NewTable(nat.DefaultConfig())

	forwarder := NewUDPForwarder(client, natTable, logger)

	// Multiple closes should not panic
	forwarder.Close()
	forwarder.Close()
	forwarder.Close()
}

// --- RawUDPForwarder Tests ---

func TestNewRawUDPForwarder(t *testing.T) {
	logger := zap.NewNop()
	client := &socks5.Client{}

	forwarder := NewRawUDPForwarder(client, logger)

	if forwarder == nil {
		t.Fatal("NewRawUDPForwarder returned nil")
	}
	if forwarder.clientHolder.Get() != client {
		t.Error("client not set correctly")
	}
	if forwarder.relayTTL() != 5*time.Minute {
		t.Errorf("relayTTL = %v, want 5m", forwarder.relayTTL())
	}
	if forwarder.relayPool == nil {
		t.Error("relayPool not initialized")
	}

	forwarder.Close()
}

func TestRawUDPForwarder_UpdateClient(t *testing.T) {
	logger := zap.NewNop()
	client1 := &socks5.Client{}
	client2 := &socks5.Client{}

	forwarder := NewRawUDPForwarder(client1, logger)
	defer forwarder.Close()

	forwarder.UpdateClient(client2)

	if forwarder.clientHolder.Get() != client2 {
		t.Error("client not updated correctly")
	}
}

func TestRawUDPForwarder_Close(t *testing.T) {
	logger := zap.NewNop()
	client := &socks5.Client{}

	forwarder := NewRawUDPForwarder(client, logger)

	// Close should not panic
	forwarder.Close()
}

func TestRawUDPForwarder_Close_Multiple(t *testing.T) {
	logger := zap.NewNop()
	client := &socks5.Client{}

	forwarder := NewRawUDPForwarder(client, logger)

	// Multiple closes should not panic
	forwarder.Close()
	forwarder.Close()
	forwarder.Close()
}

// --- RelayPool Tests ---

func TestRelayPool_GetOrCreate(t *testing.T) {
	logger := zap.NewNop()
	pool := NewRelayPool(time.Minute, logger)
	defer pool.Close()

	callCount := 0
	createFunc := func(ctx context.Context) (*socks5.UDPRelay, error) {
		callCount++
		return nil, nil // For this test we just verify creation logic
	}

	ctx := context.Background()

	// First call should create
	_, _ = pool.GetOrCreate(ctx, "test-key", createFunc)
	if callCount != 1 {
		t.Errorf("expected createFunc to be called once, got %d", callCount)
	}

	// Second call should reuse
	_, _ = pool.GetOrCreate(ctx, "test-key", createFunc)
	if callCount != 1 {
		t.Errorf("expected createFunc to not be called again, got %d calls", callCount)
	}

	// Different key should create new
	_, _ = pool.GetOrCreate(ctx, "other-key", createFunc)
	if callCount != 2 {
		t.Errorf("expected createFunc to be called for new key, got %d calls", callCount)
	}
}

func TestRelayPool_Clear(t *testing.T) {
	logger := zap.NewNop()
	pool := NewRelayPool(time.Minute, logger)
	defer pool.Close()

	// Add some entries
	ctx := context.Background()
	callCount := 0
	createFunc := func(ctx context.Context) (*socks5.UDPRelay, error) {
		callCount++
		return nil, nil
	}

	_, _ = pool.GetOrCreate(ctx, "key1", createFunc)
	_, _ = pool.GetOrCreate(ctx, "key2", createFunc)

	if callCount != 2 {
		t.Errorf("expected 2 creates, got %d", callCount)
	}

	// Clear the pool
	pool.Clear()

	// Next access should create new entries
	_, _ = pool.GetOrCreate(ctx, "key1", createFunc)
	if callCount != 3 {
		t.Errorf("expected new create after clear, got %d", callCount)
	}
}

// --- Concurrent Access Tests ---

func TestTCPForwarder_ConcurrentUpdateClient(t *testing.T) {
	logger := zap.NewNop()
	client := &socks5.Client{}
	natTable := nat.NewTable(nat.DefaultConfig())
	forwarder := NewTCPForwarder(client, natTable, logger)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			forwarder.UpdateClient(&socks5.Client{})
		}()
		go func() {
			defer wg.Done()
			_ = forwarder.clientHolder.Get()
		}()
	}
	wg.Wait()
}

func TestUDPForwarder_ConcurrentOperations(t *testing.T) {
	logger := zap.NewNop()
	client := &socks5.Client{}
	natTable := nat.NewTable(nat.DefaultConfig())
	forwarder := NewUDPForwarder(client, natTable, logger)
	defer forwarder.Close()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			forwarder.UpdateClient(&socks5.Client{})
		}()
		go func() {
			defer wg.Done()
			_ = forwarder.clientHolder.Get()
		}()
	}
	wg.Wait()
}

func TestRawUDPForwarder_ConcurrentOperations(t *testing.T) {
	logger := zap.NewNop()
	client := &socks5.Client{}
	forwarder := NewRawUDPForwarder(client, logger)
	defer forwarder.Close()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			forwarder.UpdateClient(&socks5.Client{})
		}()
		go func() {
			defer wg.Done()
			_ = forwarder.clientHolder.Get()
		}()
	}
	wg.Wait()
}

// --- Large Data Transfer Test ---

func TestBidirectionalCopy_LargeData(t *testing.T) {
	localClient, localServer := net.Pipe()
	remoteClient, remoteServer := net.Pipe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start relay
	go func() {
		bidirectionalCopy(ctx, localServer, remoteClient, DefaultTCPBufferSize)
	}()

	// Send smaller data for faster test
	dataSize := 64 * 1024 // 64KB
	largeData := make([]byte, dataSize)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	// Write and read concurrently
	errCh := make(chan error, 2)

	go func() {
		_, err := localClient.Write(largeData)
		localClient.Close()
		errCh <- err
	}()

	received := make([]byte, 0, dataSize)
	go func() {
		buf := make([]byte, 32*1024)
		for len(received) < dataSize {
			remoteServer.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, err := remoteServer.Read(buf)
			if n > 0 {
				received = append(received, buf[:n]...)
			}
			if err != nil {
				break
			}
		}
		errCh <- nil
	}()

	// Wait for both operations
	<-errCh
	<-errCh

	// Clean up
	localServer.Close()
	remoteClient.Close()
	remoteServer.Close()

	if len(received) < dataSize/2 {
		t.Errorf("received only %d bytes, expected at least %d", len(received), dataSize/2)
	}

	// Verify data integrity for received data
	for i := 0; i < min(100, len(received)); i++ {
		if received[i] != byte(i%256) {
			t.Errorf("data mismatch at index %d: got %d, want %d", i, received[i], i%256)
			break
		}
	}
}

// --- Buffer Size Tests ---

func TestTCPForwarder_BufferSize(t *testing.T) {
	logger := zap.NewNop()
	client := &socks5.Client{}
	natTable := nat.NewTable(nat.DefaultConfig())

	forwarder := NewTCPForwarder(client, natTable, logger)

	if forwarder.bufferSize != DefaultTCPBufferSize {
		t.Errorf("bufferSize = %d, want %d", forwarder.bufferSize, DefaultTCPBufferSize)
	}
}

func TestUDPForwarder_BufferSize(t *testing.T) {
	logger := zap.NewNop()
	client := &socks5.Client{}
	natTable := nat.NewTable(nat.DefaultConfig())

	forwarder := NewUDPForwarder(client, natTable, logger)
	defer forwarder.Close()

	if forwarder.bufferSize != DefaultUDPBufferSize {
		t.Errorf("bufferSize = %d, want %d", forwarder.bufferSize, DefaultUDPBufferSize)
	}
}

// --- TTL Tests ---

func TestUDPForwarder_RelayTTL(t *testing.T) {
	logger := zap.NewNop()
	client := &socks5.Client{}
	natTable := nat.NewTable(nat.DefaultConfig())

	forwarder := NewUDPForwarder(client, natTable, logger)
	defer forwarder.Close()

	expectedTTL := 5 * time.Minute
	if forwarder.relayTTL() != expectedTTL {
		t.Errorf("relayTTL = %v, want %v", forwarder.relayTTL(), expectedTTL)
	}
}

func TestRawUDPForwarder_RelayTTL(t *testing.T) {
	logger := zap.NewNop()
	client := &socks5.Client{}

	forwarder := NewRawUDPForwarder(client, logger)
	defer forwarder.Close()

	expectedTTL := 5 * time.Minute
	if forwarder.relayTTL() != expectedTTL {
		t.Errorf("relayTTL = %v, want %v", forwarder.relayTTL(), expectedTTL)
	}
}

// --- Common Helper Tests ---

func TestConnKey(t *testing.T) {
	srcAddr := &net.TCPAddr{IP: net.ParseIP("192.168.1.1"), Port: 12345}
	dstAddr := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 80}

	key := connKey(srcAddr, dstAddr)
	expected := "192.168.1.1:12345->10.0.0.1:80"

	if key != expected {
		t.Errorf("connKey = %q, want %q", key, expected)
	}
}

func TestAddrKey(t *testing.T) {
	ip := net.ParseIP("192.168.1.1")
	port := uint16(12345)

	key := addrKey(ip, port)
	expected := "192.168.1.1:12345"

	if key != expected {
		t.Errorf("addrKey = %q, want %q", key, expected)
	}
}

func TestNewSOCKS5Address_IPv4(t *testing.T) {
	ip := net.ParseIP("192.168.1.1").To4()
	port := uint16(80)

	addr := newSOCKS5Address(ip, port)

	if addr.Type != socks5.AddrTypeIPv4 {
		t.Errorf("Type = %d, want %d", addr.Type, socks5.AddrTypeIPv4)
	}
	if !addr.IP.Equal(ip) {
		t.Errorf("IP = %v, want %v", addr.IP, ip)
	}
	if addr.Port != port {
		t.Errorf("Port = %d, want %d", addr.Port, port)
	}
}

func TestNewSOCKS5Address_IPv6(t *testing.T) {
	ip := net.ParseIP("2001:db8::1")
	port := uint16(443)

	addr := newSOCKS5Address(ip, port)

	if addr.Type != socks5.AddrTypeIPv6 {
		t.Errorf("Type = %d, want %d", addr.Type, socks5.AddrTypeIPv6)
	}
	if !addr.IP.Equal(ip) {
		t.Errorf("IP = %v, want %v", addr.IP, ip)
	}
	if addr.Port != port {
		t.Errorf("Port = %d, want %d", addr.Port, port)
	}
}

func TestClientHolder(t *testing.T) {
	client1 := &socks5.Client{}
	client2 := &socks5.Client{}

	holder := &clientHolder{client: client1}

	if holder.Get() != client1 {
		t.Error("Get returned wrong client")
	}

	holder.Set(client2)

	if holder.Get() != client2 {
		t.Error("Get after Set returned wrong client")
	}
}

func TestClientHolder_Concurrent(t *testing.T) {
	holder := &clientHolder{client: &socks5.Client{}}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			holder.Set(&socks5.Client{})
		}()
		go func() {
			defer wg.Done()
			_ = holder.Get()
		}()
	}
	wg.Wait()
}

// --- Additional RelayPool Tests ---

func TestNewRelayPool(t *testing.T) {
	logger := zap.NewNop()
	ttl := 5 * time.Minute

	pool := NewRelayPool(ttl, logger)
	defer pool.Close()

	if pool == nil {
		t.Fatal("NewRelayPool returned nil")
	}
	if pool.entries == nil {
		t.Error("entries map not initialized")
	}
	if pool.ttl != ttl {
		t.Errorf("ttl = %v, want %v", pool.ttl, ttl)
	}
	if pool.logger != logger {
		t.Error("logger not set correctly")
	}
	if pool.stopCh == nil {
		t.Error("stopCh not initialized")
	}
}

func TestRelayPool_GetOrCreate_Error(t *testing.T) {
	logger := zap.NewNop()
	pool := NewRelayPool(time.Minute, logger)
	defer pool.Close()

	expectedErr := net.ErrClosed
	createFunc := func(ctx context.Context) (*socks5.UDPRelay, error) {
		return nil, expectedErr
	}

	ctx := context.Background()
	relay, err := pool.GetOrCreate(ctx, "error-key", createFunc)

	if err != expectedErr {
		t.Errorf("expected error %v, got %v", expectedErr, err)
	}
	if relay != nil {
		t.Error("expected nil relay on error")
	}

	// Verify entry was not stored
	pool.mu.Lock()
	_, exists := pool.entries["error-key"]
	pool.mu.Unlock()
	if exists {
		t.Error("entry should not be stored on creation error")
	}
}

func TestRelayPool_GetOrCreate_Concurrent(t *testing.T) {
	logger := zap.NewNop()
	pool := NewRelayPool(time.Minute, logger)
	defer pool.Close()

	var createCount int
	var mu sync.Mutex

	createFunc := func(ctx context.Context) (*socks5.UDPRelay, error) {
		mu.Lock()
		createCount++
		mu.Unlock()
		return nil, nil
	}

	ctx := context.Background()
	var wg sync.WaitGroup

	// Concurrent access to same key
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = pool.GetOrCreate(ctx, "shared-key", createFunc)
		}()
	}
	wg.Wait()

	// With lock-free creation pattern, multiple goroutines may race to create,
	// but only one relay is stored (others are closed). At least 1 create call
	// is expected, and the pool should have exactly 1 entry.
	mu.Lock()
	count := createCount
	mu.Unlock()
	if count < 1 {
		t.Errorf("expected at least 1 create call, got %d", count)
	}

	// Verify only one entry is stored
	pool.mu.Lock()
	entryCount := len(pool.entries)
	pool.mu.Unlock()
	if entryCount != 1 {
		t.Errorf("expected 1 pool entry, got %d", entryCount)
	}
}

func TestRelayPool_GetOrCreate_UpdatesLastUsed(t *testing.T) {
	logger := zap.NewNop()
	pool := NewRelayPool(time.Minute, logger)
	defer pool.Close()

	createFunc := func(ctx context.Context) (*socks5.UDPRelay, error) {
		return nil, nil
	}

	ctx := context.Background()
	_, _ = pool.GetOrCreate(ctx, "test-key", createFunc)

	// Get initial lastUsed time
	pool.mu.Lock()
	initialTime := pool.entries["test-key"].lastUsed
	pool.mu.Unlock()

	// Wait a bit and access again
	time.Sleep(10 * time.Millisecond)
	_, _ = pool.GetOrCreate(ctx, "test-key", createFunc)

	// Verify lastUsed was updated
	pool.mu.Lock()
	updatedTime := pool.entries["test-key"].lastUsed
	pool.mu.Unlock()

	if !updatedTime.After(initialTime) {
		t.Error("lastUsed time should be updated on access")
	}
}

func TestRelayPool_Cleanup(t *testing.T) {
	logger := zap.NewNop()
	// Use very short TTL for testing
	pool := NewRelayPool(10*time.Millisecond, logger)
	defer pool.Close()

	createFunc := func(ctx context.Context) (*socks5.UDPRelay, error) {
		return nil, nil
	}

	ctx := context.Background()
	_, _ = pool.GetOrCreate(ctx, "expire-key", createFunc)

	// Verify entry exists
	pool.mu.Lock()
	_, exists := pool.entries["expire-key"]
	pool.mu.Unlock()
	if !exists {
		t.Fatal("entry should exist initially")
	}

	// Wait for TTL to expire
	time.Sleep(20 * time.Millisecond)

	// Trigger cleanup
	pool.cleanup()

	// Verify entry was removed
	pool.mu.Lock()
	_, exists = pool.entries["expire-key"]
	pool.mu.Unlock()
	if exists {
		t.Error("expired entry should be removed after cleanup")
	}
}

func TestRelayPool_Cleanup_KeepsRecent(t *testing.T) {
	logger := zap.NewNop()
	pool := NewRelayPool(time.Hour, logger) // Long TTL
	defer pool.Close()

	createFunc := func(ctx context.Context) (*socks5.UDPRelay, error) {
		return nil, nil
	}

	ctx := context.Background()
	_, _ = pool.GetOrCreate(ctx, "recent-key", createFunc)

	// Trigger cleanup
	pool.cleanup()

	// Verify entry still exists (TTL not expired)
	pool.mu.Lock()
	_, exists := pool.entries["recent-key"]
	pool.mu.Unlock()
	if !exists {
		t.Error("recent entry should not be removed")
	}
}

func TestRelayPool_Close_Idempotent(t *testing.T) {
	logger := zap.NewNop()
	pool := NewRelayPool(time.Minute, logger)

	// Multiple closes should not panic
	pool.Close()
	pool.Close()
	pool.Close()
}

func TestRelayPool_Close_StopsCleanupLoop(t *testing.T) {
	logger := zap.NewNop()
	pool := NewRelayPool(time.Minute, logger)

	// Close the pool
	pool.Close()

	// Verify stopCh is closed (attempting to close again would panic if not using stopOnce)
	select {
	case <-pool.stopCh:
		// Expected - channel is closed
	default:
		t.Error("stopCh should be closed after Close()")
	}
}

func TestRelayPool_NilLogger(t *testing.T) {
	// Should not panic with nil logger
	pool := NewRelayPool(10*time.Millisecond, nil)
	defer pool.Close()

	createFunc := func(ctx context.Context) (*socks5.UDPRelay, error) {
		return nil, nil
	}

	ctx := context.Background()
	_, _ = pool.GetOrCreate(ctx, "test-key", createFunc)

	// Wait for TTL to expire
	time.Sleep(20 * time.Millisecond)

	// Trigger cleanup - should not panic even with nil logger
	pool.cleanup()
}

// --- Additional Common Helper Tests ---

func TestConnKey_IPv6(t *testing.T) {
	srcAddr := &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 12345}
	dstAddr := &net.TCPAddr{IP: net.ParseIP("2001:db8::2"), Port: 443}

	key := connKey(srcAddr, dstAddr)
	expected := "[2001:db8::1]:12345->[2001:db8::2]:443"

	if key != expected {
		t.Errorf("connKey = %q, want %q", key, expected)
	}
}

func TestAddrKey_IPv6(t *testing.T) {
	ip := net.ParseIP("2001:db8::1")
	port := uint16(443)

	key := addrKey(ip, port)
	expected := "2001:db8::1:443"

	if key != expected {
		t.Errorf("addrKey = %q, want %q", key, expected)
	}
}

func TestNewSOCKS5Address_IPv4Mapped(t *testing.T) {
	// IPv4-mapped IPv6 address should be treated as IPv4
	ip := net.ParseIP("::ffff:192.168.1.1")
	port := uint16(80)

	addr := newSOCKS5Address(ip, port)

	// To4() returns non-nil for IPv4-mapped addresses
	if addr.Type != socks5.AddrTypeIPv4 {
		t.Errorf("Type = %d, want %d (IPv4-mapped should be treated as IPv4)", addr.Type, socks5.AddrTypeIPv4)
	}
}

func TestClientHolder_NilClient(t *testing.T) {
	holder := &clientHolder{}

	if holder.Get() != nil {
		t.Error("Get should return nil for uninitialized holder")
	}

	holder.Set(nil)
	if holder.Get() != nil {
		t.Error("Get should return nil after setting nil")
	}
}

// --- Benchmark Tests ---

func BenchmarkConnKey(b *testing.B) {
	srcAddr := &net.TCPAddr{IP: net.ParseIP("192.168.1.1"), Port: 12345}
	dstAddr := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 80}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = connKey(srcAddr, dstAddr)
	}
}

func BenchmarkAddrKey(b *testing.B) {
	ip := net.ParseIP("192.168.1.1")
	port := uint16(12345)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = addrKey(ip, port)
	}
}

func BenchmarkNewSOCKS5Address(b *testing.B) {
	ip := net.ParseIP("192.168.1.1").To4()
	port := uint16(80)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = newSOCKS5Address(ip, port)
	}
}

func BenchmarkRelayPool_GetOrCreate(b *testing.B) {
	logger := zap.NewNop()
	pool := NewRelayPool(time.Minute, logger)
	defer pool.Close()

	createFunc := func(ctx context.Context) (*socks5.UDPRelay, error) {
		return nil, nil
	}

	ctx := context.Background()
	// Pre-create the entry
	_, _ = pool.GetOrCreate(ctx, "bench-key", createFunc)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = pool.GetOrCreate(ctx, "bench-key", createFunc)
	}
}

func BenchmarkClientHolder_GetSet(b *testing.B) {
	holder := &clientHolder{client: &socks5.Client{}}
	client := &socks5.Client{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		holder.Set(client)
		_ = holder.Get()
	}
}
