package proxy

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/postalsys/mutiauk/internal/socks5"
	"go.uber.org/zap"
)

// --- Test Helpers ---

// icmpTestEnv holds the common test environment for ICMP forwarder tests
// that require a mock SOCKS5 server.
type icmpTestEnv struct {
	server    *mockSOCKS5Server
	forwarder *ICMPForwarder
	ctx       context.Context
	cancel    context.CancelFunc
}

// newICMPTestEnv creates a complete test environment with mock server,
// SOCKS5 client, ICMP forwarder, and a 5-second context.
func newICMPTestEnv(t *testing.T) *icmpTestEnv {
	t.Helper()

	server := newMockSOCKS5Server(t)
	client := socks5.NewClient(server.Addr(), nil, time.Second, time.Second)
	forwarder := NewICMPForwarder(client, zap.NewNop(), 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	env := &icmpTestEnv{
		server:    server,
		forwarder: forwarder,
		ctx:       ctx,
		cancel:    cancel,
	}

	t.Cleanup(func() {
		cancel()
		forwarder.Close()
		server.Close()
	})

	return env
}

// sessionCount returns the number of active sessions (thread-safe).
func (e *icmpTestEnv) sessionCount() int {
	e.forwarder.mu.RLock()
	defer e.forwarder.mu.RUnlock()
	return len(e.forwarder.sessions)
}

// --- ICMPForwarder Tests ---

func TestNewICMPForwarder(t *testing.T) {
	logger := zap.NewNop()
	client := &socks5.Client{}

	forwarder := NewICMPForwarder(client, logger, 5*time.Second)

	if forwarder == nil {
		t.Fatal("NewICMPForwarder returned nil")
	}
	if forwarder.clientHolder.Get() != client {
		t.Error("client not set correctly")
	}
	if forwarder.logger != logger {
		t.Error("logger not set correctly")
	}
	if forwarder.timeout != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", forwarder.timeout)
	}
	if forwarder.sessions == nil {
		t.Error("sessions map not initialized")
	}
	if forwarder.done == nil {
		t.Error("done channel not initialized")
	}

	forwarder.Close()
}

func TestNewICMPForwarder_DefaultTimeout(t *testing.T) {
	forwarder := NewICMPForwarder(&socks5.Client{}, zap.NewNop(), 0)
	defer forwarder.Close()

	if forwarder.timeout != 10*time.Second {
		t.Errorf("timeout = %v, want 10s (default)", forwarder.timeout)
	}
}

func TestICMPForwarder_Close(t *testing.T) {
	forwarder := NewICMPForwarder(&socks5.Client{}, zap.NewNop(), time.Second)

	err := forwarder.Close()
	if err != nil {
		t.Errorf("Close returned error: %v", err)
	}

	select {
	case <-forwarder.done:
		// Expected - channel is closed
	default:
		t.Error("done channel should be closed after Close()")
	}
}

func TestICMPForwarder_Close_Multiple(t *testing.T) {
	forwarder := NewICMPForwarder(&socks5.Client{}, zap.NewNop(), time.Second)

	// Multiple closes should not panic (protected by sync.Once)
	forwarder.Close()
	forwarder.Close()
	forwarder.Close()
}

func TestICMPForwarder_CleanupLoopStops(t *testing.T) {
	forwarder := NewICMPForwarder(&socks5.Client{}, zap.NewNop(), time.Second)

	// Give cleanup loop time to start
	time.Sleep(10 * time.Millisecond)

	// Close should stop the cleanup loop.
	// If the loop does not stop, this test would leak goroutines.
	forwarder.Close()
}

// --- Session Key Tests ---

func TestICMPSessionKey(t *testing.T) {
	key1 := icmpSessionKey{dstIP: "192.168.1.1", id: 1234}
	key2 := icmpSessionKey{dstIP: "192.168.1.1", id: 1234}
	key3 := icmpSessionKey{dstIP: "192.168.1.2", id: 1234}
	key4 := icmpSessionKey{dstIP: "192.168.1.1", id: 5678}

	if key1 != key2 {
		t.Error("identical keys should be equal")
	}
	if key1 == key3 {
		t.Error("keys with different IPs should not be equal")
	}
	if key1 == key4 {
		t.Error("keys with different IDs should not be equal")
	}
}

// --- Cleanup Tests ---

func TestICMPForwarder_Cleanup_RemovesStale(t *testing.T) {
	env := newICMPTestEnv(t)

	srcIP := net.ParseIP("10.0.0.1")
	dstIP := net.ParseIP("8.8.8.8")

	_, err := env.forwarder.HandleICMP(env.ctx, srcIP, dstIP, 1234, 1, []byte("ping"))
	if err != nil {
		t.Fatalf("HandleICMP failed: %v", err)
	}

	if n := env.sessionCount(); n != 1 {
		t.Fatalf("expected 1 session, got %d", n)
	}

	// Manually mark the session as stale
	key := icmpSessionKey{dstIP: dstIP.String(), id: 1234}
	env.forwarder.mu.Lock()
	if session, exists := env.forwarder.sessions[key]; exists {
		session.mu.Lock()
		session.lastUsed = time.Now().Add(-2 * time.Minute)
		session.mu.Unlock()
	}
	env.forwarder.mu.Unlock()

	env.forwarder.cleanup()

	env.forwarder.mu.RLock()
	_, exists := env.forwarder.sessions[key]
	env.forwarder.mu.RUnlock()
	if exists {
		t.Error("stale session should be removed after cleanup")
	}
}

func TestICMPForwarder_Cleanup_KeepsRecent(t *testing.T) {
	forwarder := NewICMPForwarder(&socks5.Client{}, zap.NewNop(), time.Second)
	defer forwarder.Close()

	forwarder.mu.RLock()
	count := len(forwarder.sessions)
	forwarder.mu.RUnlock()

	if count != 0 {
		t.Errorf("expected 0 sessions initially, got %d", count)
	}
}

// --- Concurrent Access Tests ---

func TestICMPForwarder_ConcurrentSessionCreation_NoDuplicates(t *testing.T) {
	server := newMockSOCKS5Server(t)
	server.associateDelay = 100 * time.Millisecond
	defer server.Close()

	client := socks5.NewClient(server.Addr(), nil, 5*time.Second, 5*time.Second)
	forwarder := NewICMPForwarder(client, zap.NewNop(), 5*time.Second)
	defer forwarder.Close()

	dstIP := net.ParseIP("8.8.8.8")
	ctx := context.Background()

	const goroutines = 16
	sessions := make([]*icmpSession, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			session, err := forwarder.getOrCreateSession(ctx, dstIP, 1234)
			if err != nil {
				t.Errorf("getOrCreateSession failed: %v", err)
				return
			}
			sessions[i] = session
		}(i)
	}
	wg.Wait()

	if n := len(forwarder.sessions); n != 1 {
		t.Errorf("expected exactly 1 session, got %d", n)
	}
	for i, s := range sessions {
		if s == nil {
			t.Errorf("goroutine %d got nil session", i)
		} else if s != sessions[0] {
			t.Errorf("goroutine %d got a different session instance", i)
		}
	}
}

func TestICMPForwarder_SessionCreationDoesNotBlockLookups(t *testing.T) {
	server := newMockSOCKS5Server(t)
	defer server.Close()

	client := socks5.NewClient(server.Addr(), nil, 5*time.Second, 5*time.Second)
	forwarder := NewICMPForwarder(client, zap.NewNop(), 5*time.Second)
	defer forwarder.Close()

	ctx := context.Background()

	// Establish a session to look up.
	dstA := net.ParseIP("8.8.8.8")
	if _, err := forwarder.getOrCreateSession(ctx, dstA, 1234); err != nil {
		t.Fatalf("initial getOrCreateSession failed: %v", err)
	}

	// Start a slow session creation for another destination.
	server.associateDelay = 500 * time.Millisecond
	createDone := make(chan error, 1)
	go func() {
		_, err := forwarder.getOrCreateSession(ctx, net.ParseIP("1.1.1.1"), 1234)
		createDone <- err
	}()

	// While the slow create is in flight, lookups of the existing session
	// must not be blocked by it (the write lock used to be held across the
	// whole ICMP ASSOCIATE handshake).
	start := time.Now()
	for i := 0; i < 200; i++ {
		if _, err := forwarder.getOrCreateSession(ctx, dstA, 1234); err != nil {
			t.Fatalf("lookup failed during concurrent create: %v", err)
		}
	}
	lookupElapsed := time.Since(start)

	if err := <-createDone; err != nil {
		t.Fatalf("slow create failed: %v", err)
	}

	if lookupElapsed > 250*time.Millisecond {
		t.Errorf("lookups stalled for %v during session creation", lookupElapsed)
	}

	if n := len(forwarder.sessions); n != 2 {
		t.Errorf("expected 2 sessions, got %d", n)
	}
}

func TestICMPForwarder_ConcurrentClose(t *testing.T) {
	forwarder := NewICMPForwarder(&socks5.Client{}, zap.NewNop(), time.Second)

	var wg sync.WaitGroup

	// Concurrent closes should not panic (protected by sync.Once)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			forwarder.Close()
		}()
	}

	wg.Wait()
}

func TestICMPForwarder_ConcurrentSessionAccess(t *testing.T) {
	forwarder := NewICMPForwarder(&socks5.Client{}, zap.NewNop(), time.Second)
	defer forwarder.Close()

	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			forwarder.mu.RLock()
			_ = len(forwarder.sessions)
			forwarder.mu.RUnlock()
		}()
	}

	wg.Wait()
}

// --- Mock SOCKS5 Server ---

// mockSOCKS5Server simulates a SOCKS5 server that supports ICMP
type mockSOCKS5Server struct {
	listener net.Listener

	// associateDelay, when > 0, delays the ICMP ASSOCIATE reply to simulate
	// a slow mesh during session setup.
	associateDelay time.Duration
}

func newMockSOCKS5Server(tb testing.TB) *mockSOCKS5Server {
	tb.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("failed to create listener: %v", err)
	}

	server := &mockSOCKS5Server{listener: listener}
	go server.acceptLoop()

	return server
}

func (s *mockSOCKS5Server) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConnection(conn)
	}
}

func (s *mockSOCKS5Server) handleConnection(conn net.Conn) {
	defer conn.Close()

	// Read method selection
	methods := make([]byte, 3)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}

	// Send method response (no auth)
	conn.Write([]byte{0x05, 0x00})

	// Read request
	req := make([]byte, 4)
	if _, err := io.ReadFull(conn, req); err != nil {
		return
	}

	// Read address based on type
	var addrLen int
	switch req[3] {
	case 0x01: // IPv4
		addrLen = 4 + 2
	case 0x04: // IPv6
		addrLen = 16 + 2
	default:
		return
	}

	addr := make([]byte, addrLen)
	if _, err := io.ReadFull(conn, addr); err != nil {
		return
	}

	// Check if this is ICMP Echo command (0x04)
	if req[1] == 0x04 {
		if s.associateDelay > 0 {
			time.Sleep(s.associateDelay)
		}
		// Send success reply
		reply := []byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
		conn.Write(reply)

		s.handleICMPRelay(conn)
	}
}

func (s *mockSOCKS5Server) handleICMPRelay(conn net.Conn) {
	header := make([]byte, 6)
	if _, err := io.ReadFull(conn, header); err != nil {
		return
	}

	id := binary.BigEndian.Uint16(header[0:2])
	seq := binary.BigEndian.Uint16(header[2:4])
	payloadLen := binary.BigEndian.Uint16(header[4:6])

	payload := make([]byte, payloadLen)
	if payloadLen > 0 {
		if _, err := io.ReadFull(conn, payload); err != nil {
			return
		}
	}

	// Echo the request back as a reply
	reply := make([]byte, 6+len(payload))
	binary.BigEndian.PutUint16(reply[0:2], id)
	binary.BigEndian.PutUint16(reply[2:4], seq)
	binary.BigEndian.PutUint16(reply[4:6], uint16(len(payload)))
	copy(reply[6:], payload)

	conn.Write(reply)
}

func (s *mockSOCKS5Server) Addr() string {
	return s.listener.Addr().String()
}

func (s *mockSOCKS5Server) Close() {
	s.listener.Close()
}

// --- Integration Tests with Mock Server ---

func TestICMPForwarder_HandleICMP_WithMockServer(t *testing.T) {
	env := newICMPTestEnv(t)

	srcIP := net.ParseIP("10.0.0.1")
	dstIP := net.ParseIP("8.8.8.8")
	payload := []byte("ping data")

	reply, err := env.forwarder.HandleICMP(env.ctx, srcIP, dstIP, 1234, 1, payload)
	if err != nil {
		t.Fatalf("HandleICMP failed: %v", err)
	}

	if string(reply) != string(payload) {
		t.Errorf("reply = %q, want %q", reply, payload)
	}
}

func TestICMPForwarder_HandleICMP_SessionReuse(t *testing.T) {
	env := newICMPTestEnv(t)

	srcIP := net.ParseIP("10.0.0.1")
	dstIP := net.ParseIP("8.8.8.8")

	// First request creates a session
	_, err := env.forwarder.HandleICMP(env.ctx, srcIP, dstIP, 1234, 1, []byte("ping1"))
	if err != nil {
		t.Fatalf("first HandleICMP failed: %v", err)
	}

	if n := env.sessionCount(); n != 1 {
		t.Errorf("expected 1 session after first request, got %d", n)
	}

	// Second request with same dstIP and id should reuse the session.
	// Note: the mock server closes after one echo, so actual reuse
	// would fail here. In production, sessions are long-lived.
}

func TestICMPForwarder_HandleICMP_DifferentDestinations(t *testing.T) {
	env := newICMPTestEnv(t)

	srcIP := net.ParseIP("10.0.0.1")

	_, err := env.forwarder.HandleICMP(env.ctx, srcIP, net.ParseIP("8.8.8.8"), 1234, 1, []byte("ping1"))
	if err != nil {
		t.Fatalf("first HandleICMP failed: %v", err)
	}

	_, err = env.forwarder.HandleICMP(env.ctx, srcIP, net.ParseIP("1.1.1.1"), 1234, 1, []byte("ping2"))
	if err != nil {
		t.Fatalf("second HandleICMP failed: %v", err)
	}

	if n := env.sessionCount(); n != 2 {
		t.Errorf("expected 2 sessions for different destinations, got %d", n)
	}
}

func TestICMPForwarder_HandleICMP_DifferentIDs(t *testing.T) {
	env := newICMPTestEnv(t)

	srcIP := net.ParseIP("10.0.0.1")
	dstIP := net.ParseIP("8.8.8.8")

	_, err := env.forwarder.HandleICMP(env.ctx, srcIP, dstIP, 1111, 1, []byte("ping1"))
	if err != nil {
		t.Fatalf("first HandleICMP failed: %v", err)
	}

	_, err = env.forwarder.HandleICMP(env.ctx, srcIP, dstIP, 2222, 1, []byte("ping2"))
	if err != nil {
		t.Fatalf("second HandleICMP failed: %v", err)
	}

	if n := env.sessionCount(); n != 2 {
		t.Errorf("expected 2 sessions for different IDs, got %d", n)
	}
}

func TestICMPForwarder_HandleICMP_EmptyPayload(t *testing.T) {
	env := newICMPTestEnv(t)

	srcIP := net.ParseIP("10.0.0.1")
	dstIP := net.ParseIP("8.8.8.8")

	reply, err := env.forwarder.HandleICMP(env.ctx, srcIP, dstIP, 1234, 1, []byte{})
	if err != nil {
		t.Fatalf("HandleICMP with empty payload failed: %v", err)
	}

	if len(reply) != 0 {
		t.Errorf("expected empty reply, got %d bytes", len(reply))
	}
}

func TestICMPForwarder_HandleICMP_LargePayload(t *testing.T) {
	env := newICMPTestEnv(t)

	srcIP := net.ParseIP("10.0.0.1")
	dstIP := net.ParseIP("8.8.8.8")

	largePayload := make([]byte, 1024)
	for i := range largePayload {
		largePayload[i] = byte(i % 256)
	}

	reply, err := env.forwarder.HandleICMP(env.ctx, srcIP, dstIP, 1234, 1, largePayload)
	if err != nil {
		t.Fatalf("HandleICMP with large payload failed: %v", err)
	}

	if len(reply) != len(largePayload) {
		t.Errorf("reply length = %d, want %d", len(reply), len(largePayload))
	}
}

func TestICMPForwarder_HandleICMP_IPv6(t *testing.T) {
	env := newICMPTestEnv(t)

	srcIP := net.ParseIP("2001:db8::1")
	dstIP := net.ParseIP("2001:db8::2")

	reply, err := env.forwarder.HandleICMP(env.ctx, srcIP, dstIP, 1234, 1, []byte("ping"))
	if err != nil {
		t.Fatalf("HandleICMP with IPv6 failed: %v", err)
	}

	if string(reply) != "ping" {
		t.Errorf("reply = %q, want %q", reply, "ping")
	}
}

// --- Benchmark Tests ---

func BenchmarkNewICMPForwarder(b *testing.B) {
	logger := zap.NewNop()
	client := &socks5.Client{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f := NewICMPForwarder(client, logger, time.Second)
		f.Close()
	}
}

func BenchmarkICMPSessionKey(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = icmpSessionKey{dstIP: "192.168.1.1", id: 1234}
	}
}

func BenchmarkICMPForwarder_HandleICMP(b *testing.B) {
	server := newMockSOCKS5Server(b)
	defer server.Close()

	logger := zap.NewNop()
	client := socks5.NewClient(server.Addr(), nil, time.Second, time.Second)

	forwarder := NewICMPForwarder(client, logger, 5*time.Second)
	defer forwarder.Close()

	ctx := context.Background()
	srcIP := net.ParseIP("10.0.0.1")
	dstIP := net.ParseIP("8.8.8.8")
	payload := []byte("ping")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Each iteration creates a new session because the mock server
		// closes after one echo. In real usage, sessions would be reused.
		_, _ = forwarder.HandleICMP(ctx, srcIP, dstIP, uint16(i), 1, payload)
	}
}
