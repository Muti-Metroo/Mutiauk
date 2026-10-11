package proxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/postalsys/mutiauk/internal/socks5"
	"go.uber.org/zap"
)

// mockUDPDatagram is a UDP datagram received by the mock SOCKS5 server on its
// relay socket, with the SOCKS5 UDP request header parsed out.
type mockUDPDatagram struct {
	clientAddr *net.UDPAddr
	target     string
	payload    []byte
}

// mockUDPSOCKS5Server is a minimal SOCKS5 server that supports UDP ASSOCIATE
// (no auth) for testing RawUDPForwarder. Received relay datagrams are exposed
// on the datagrams channel; tests inject responses with respond().
type mockUDPSOCKS5Server struct {
	t         *testing.T
	tcpLn     net.Listener
	udpConn   *net.UDPConn
	datagrams chan mockUDPDatagram

	mu           sync.Mutex
	associates   int
	controlConns []net.Conn
}

func newMockUDPSOCKS5Server(t *testing.T) *mockUDPSOCKS5Server {
	t.Helper()

	tcpLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start mock TCP listener: %v", err)
	}
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		tcpLn.Close()
		t.Fatalf("failed to start mock UDP socket: %v", err)
	}

	s := &mockUDPSOCKS5Server{
		t:         t,
		tcpLn:     tcpLn,
		udpConn:   udpConn,
		datagrams: make(chan mockUDPDatagram, 64),
	}

	go s.acceptLoop()
	go s.udpReadLoop()

	t.Cleanup(s.close)
	return s
}

func (s *mockUDPSOCKS5Server) addr() string {
	return s.tcpLn.Addr().String()
}

func (s *mockUDPSOCKS5Server) close() {
	s.tcpLn.Close()
	s.udpConn.Close()
	s.mu.Lock()
	for _, c := range s.controlConns {
		c.Close()
	}
	s.mu.Unlock()
}

func (s *mockUDPSOCKS5Server) associateCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.associates
}

// closeControlConns closes all server-side control connections, simulating a
// dead agent tearing down its UDP associations.
func (s *mockUDPSOCKS5Server) closeControlConns() {
	s.mu.Lock()
	conns := s.controlConns
	s.controlConns = nil
	s.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

func (s *mockUDPSOCKS5Server) acceptLoop() {
	for {
		conn, err := s.tcpLn.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *mockUDPSOCKS5Server) handleConn(conn net.Conn) {
	// Method selection: VER NMETHODS METHODS...
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		conn.Close()
		return
	}
	if _, err := io.ReadFull(conn, make([]byte, int(head[1]))); err != nil {
		conn.Close()
		return
	}
	if _, err := conn.Write([]byte{socks5.Version, 0x00}); err != nil {
		conn.Close()
		return
	}

	// Request: VER CMD RSV ATYP DST.ADDR DST.PORT
	req := make([]byte, 4)
	if _, err := io.ReadFull(conn, req); err != nil {
		conn.Close()
		return
	}
	if req[1] != socks5.CmdUDPAssociate {
		conn.Close()
		return
	}
	if err := skipAddress(conn, req[3]); err != nil {
		conn.Close()
		return
	}

	// Reply success with the UDP relay address as BND.ADDR.
	udpAddr := s.udpConn.LocalAddr().(*net.UDPAddr)
	reply := []byte{socks5.Version, socks5.ReplySucceeded, 0x00, socks5.AddrTypeIPv4,
		127, 0, 0, 1, byte(udpAddr.Port >> 8), byte(udpAddr.Port)}
	if _, err := conn.Write(reply); err != nil {
		conn.Close()
		return
	}

	s.mu.Lock()
	s.associates++
	s.controlConns = append(s.controlConns, conn)
	s.mu.Unlock()
}

// skipAddress consumes an address (ATYP already read) plus the port from conn.
func skipAddress(conn net.Conn, atyp byte) error {
	var n int
	switch atyp {
	case socks5.AddrTypeIPv4:
		n = 4
	case socks5.AddrTypeIPv6:
		n = 16
	case socks5.AddrTypeDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return err
		}
		n = int(lenBuf[0])
	default:
		return fmt.Errorf("unknown atyp %d", atyp)
	}
	_, err := io.ReadFull(conn, make([]byte, n+2))
	return err
}

func (s *mockUDPSOCKS5Server) udpReadLoop() {
	buf := make([]byte, 65535)
	for {
		n, clientAddr, err := s.udpConn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		dg, err := parseRelayDatagram(buf[:n])
		if err != nil {
			continue
		}
		dg.clientAddr = clientAddr
		select {
		case s.datagrams <- dg:
		default:
		}
	}
}

// parseRelayDatagram parses a SOCKS5 UDP request datagram (FRAG must be 0).
func parseRelayDatagram(buf []byte) (mockUDPDatagram, error) {
	var dg mockUDPDatagram
	if len(buf) < 4+6 {
		return dg, fmt.Errorf("datagram too short: %d", len(buf))
	}
	if buf[2] != 0 {
		return dg, fmt.Errorf("unexpected FRAG %d", buf[2])
	}
	var target string
	var dataStart int
	switch buf[3] {
	case socks5.AddrTypeIPv4:
		target = fmt.Sprintf("%d.%d.%d.%d:%d", buf[4], buf[5], buf[6], buf[7],
			binary.BigEndian.Uint16(buf[8:10]))
		dataStart = 10
	case socks5.AddrTypeIPv6:
		if len(buf) < 4+18 {
			return dg, fmt.Errorf("datagram too short for IPv6: %d", len(buf))
		}
		target = fmt.Sprintf("[%s]:%d", net.IP(buf[4:20]).String(), binary.BigEndian.Uint16(buf[20:22]))
		dataStart = 22
	default:
		return dg, fmt.Errorf("unsupported atyp %d", buf[3])
	}
	dg.target = target
	dg.payload = append([]byte(nil), buf[dataStart:]...)
	return dg, nil
}

// nextDatagram waits for the next relay datagram from a client.
func (s *mockUDPSOCKS5Server) nextDatagram(t *testing.T) mockUDPDatagram {
	t.Helper()
	select {
	case dg := <-s.datagrams:
		return dg
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for relay datagram")
		return mockUDPDatagram{}
	}
}

// respond sends a UDP response datagram to clientAddr, sourced from the given
// responder address in the SOCKS5 header.
func (s *mockUDPSOCKS5Server) respond(t *testing.T, clientAddr *net.UDPAddr, responderIP string, responderPort uint16, payload []byte) {
	t.Helper()
	ip := net.ParseIP(responderIP).To4()
	if ip == nil {
		t.Fatalf("mock respond only supports IPv4 responders, got %q", responderIP)
	}
	packet := make([]byte, 0, 10+len(payload))
	packet = append(packet, 0, 0, 0, socks5.AddrTypeIPv4)
	packet = append(packet, ip...)
	packet = binary.BigEndian.AppendUint16(packet, responderPort)
	packet = append(packet, payload...)
	if _, err := s.udpConn.WriteToUDP(packet, clientAddr); err != nil {
		t.Fatalf("failed to send mock response: %v", err)
	}
}

// replyRecord captures one invocation of a stack.UDPReplyFunc.
type replyRecord struct {
	responderIP   net.IP
	clientIP      net.IP
	responderPort uint16
	clientPort    uint16
	payload       []byte
}

// replyCollector collects reply callback invocations on a channel.
func newReplyCollector() (chan replyRecord, func(responderIP, clientIP net.IP, responderPort, clientPort uint16, payload []byte)) {
	ch := make(chan replyRecord, 64)
	return ch, func(responderIP, clientIP net.IP, responderPort, clientPort uint16, payload []byte) {
		ch <- replyRecord{
			responderIP:   append(net.IP(nil), responderIP...),
			clientIP:      append(net.IP(nil), clientIP...),
			responderPort: responderPort,
			clientPort:    clientPort,
			payload:       append([]byte(nil), payload...),
		}
	}
}

func nextReply(t *testing.T, ch chan replyRecord) replyRecord {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for UDP reply")
		return replyRecord{}
	}
}

func expectNoReply(t *testing.T, ch chan replyRecord, wait time.Duration) {
	t.Helper()
	select {
	case r := <-ch:
		t.Fatalf("unexpected reply from %s:%d", r.responderIP, r.responderPort)
	case <-time.After(wait):
	}
}

func newTestRawUDPForwarder(t *testing.T, server *mockUDPSOCKS5Server) *RawUDPForwarder {
	t.Helper()
	client := socks5.NewClient(server.addr(), nil, 5*time.Second, 60*time.Second)
	fwd := NewRawUDPForwarder(client, zap.NewNop())
	t.Cleanup(fwd.Close)
	return fwd
}

var (
	testClientIP   = net.ParseIP("10.200.200.2")
	testClientPort = uint16(5300)
)

func TestRawUDPForwarder_MultiDatagramResponse(t *testing.T) {
	server := newMockUDPSOCKS5Server(t)
	fwd := newTestRawUDPForwarder(t, server)
	replies, replyFn := newReplyCollector()

	ctx := context.Background()
	if err := fwd.HandleRawUDP(ctx, testClientIP, net.ParseIP("8.8.8.8"), testClientPort, 53, []byte("query"), replyFn); err != nil {
		t.Fatalf("HandleRawUDP failed: %v", err)
	}

	dg := server.nextDatagram(t)
	if dg.target != "8.8.8.8:53" {
		t.Fatalf("target = %q, want 8.8.8.8:53", dg.target)
	}

	// A single request may legitimately produce several response datagrams
	// (e.g. a QUIC server flight); all of them must be delivered.
	for _, payload := range []string{"resp-1", "resp-2", "resp-3"} {
		server.respond(t, dg.clientAddr, "8.8.8.8", 53, []byte(payload))
	}

	for i, want := range []string{"resp-1", "resp-2", "resp-3"} {
		r := nextReply(t, replies)
		if string(r.payload) != want {
			t.Errorf("reply %d payload = %q, want %q", i, r.payload, want)
		}
		if !r.responderIP.Equal(net.ParseIP("8.8.8.8")) || r.responderPort != 53 {
			t.Errorf("reply %d responder = %s:%d, want 8.8.8.8:53", i, r.responderIP, r.responderPort)
		}
		if !r.clientIP.Equal(testClientIP) || r.clientPort != testClientPort {
			t.Errorf("reply %d client = %s:%d, want %s:%d", i, r.clientIP, r.clientPort, testClientIP, testClientPort)
		}
	}
}

func TestRawUDPForwarder_ResponseAttributedToResponder(t *testing.T) {
	server := newMockUDPSOCKS5Server(t)
	fwd := newTestRawUDPForwarder(t, server)
	replies, replyFn := newReplyCollector()

	ctx := context.Background()

	// Two destinations from the same source port share one SOCKS5 UDP
	// association; a response must be attributed to the actual responder,
	// never to the request's destination.
	if err := fwd.HandleRawUDP(ctx, testClientIP, net.ParseIP("8.8.8.8"), testClientPort, 53, []byte("query-a"), replyFn); err != nil {
		t.Fatalf("HandleRawUDP failed: %v", err)
	}
	dgA := server.nextDatagram(t)

	if err := fwd.HandleRawUDP(ctx, testClientIP, net.ParseIP("1.1.1.1"), testClientPort, 53, []byte("query-b"), replyFn); err != nil {
		t.Fatalf("HandleRawUDP failed: %v", err)
	}
	dgB := server.nextDatagram(t)

	// Both datagrams must ride the same association (one UDP ASSOCIATE).
	if dgA.clientAddr.String() != dgB.clientAddr.String() {
		t.Fatalf("expected a shared relay socket, got %s and %s", dgA.clientAddr, dgB.clientAddr)
	}

	// Only B answers.
	server.respond(t, dgB.clientAddr, "1.1.1.1", 53, []byte("answer-b"))

	r := nextReply(t, replies)
	if !r.responderIP.Equal(net.ParseIP("1.1.1.1")) || r.responderPort != 53 {
		t.Errorf("responder = %s:%d, want 1.1.1.1:53", r.responderIP, r.responderPort)
	}
	if string(r.payload) != "answer-b" {
		t.Errorf("payload = %q, want %q", r.payload, "answer-b")
	}
	expectNoReply(t, replies, 200*time.Millisecond)
}

func TestRawUDPForwarder_LateResponseKeepsResponderAddress(t *testing.T) {
	server := newMockUDPSOCKS5Server(t)
	fwd := newTestRawUDPForwarder(t, server)
	replies, replyFn := newReplyCollector()

	ctx := context.Background()

	// Query A, which stays silent for now.
	if err := fwd.HandleRawUDP(ctx, testClientIP, net.ParseIP("8.8.8.8"), testClientPort, 53, []byte("query-a"), replyFn); err != nil {
		t.Fatalf("HandleRawUDP failed: %v", err)
	}
	dg := server.nextDatagram(t)

	// Query B, which answers immediately.
	if err := fwd.HandleRawUDP(ctx, testClientIP, net.ParseIP("1.1.1.1"), testClientPort, 53, []byte("query-b"), replyFn); err != nil {
		t.Fatalf("HandleRawUDP failed: %v", err)
	}
	server.nextDatagram(t)
	server.respond(t, dg.clientAddr, "1.1.1.1", 53, []byte("answer-b"))

	r := nextReply(t, replies)
	if !r.responderIP.Equal(net.ParseIP("1.1.1.1")) {
		t.Fatalf("first reply responder = %s, want 1.1.1.1", r.responderIP)
	}

	// A's response arrives late. It must still be attributed to A - never
	// consumed as the answer to the query sent to B.
	server.respond(t, dg.clientAddr, "8.8.8.8", 53, []byte("late-answer-a"))

	r = nextReply(t, replies)
	if !r.responderIP.Equal(net.ParseIP("8.8.8.8")) || r.responderPort != 53 {
		t.Errorf("late reply responder = %s:%d, want 8.8.8.8:53", r.responderIP, r.responderPort)
	}
	if string(r.payload) != "late-answer-a" {
		t.Errorf("late reply payload = %q, want %q", r.payload, "late-answer-a")
	}
	expectNoReply(t, replies, 200*time.Millisecond)
}

func TestRawUDPForwarder_OneWayTrafficDoesNotBlock(t *testing.T) {
	server := newMockUDPSOCKS5Server(t)
	fwd := newTestRawUDPForwarder(t, server)
	_, replyFn := newReplyCollector()

	ctx := context.Background()

	// One-way traffic (no responder) must not block the forwarding goroutine:
	// with the old strict request/response model each of these calls would
	// have burned a full 5s receive deadline.
	start := time.Now()
	const datagrams = 20
	for i := 0; i < datagrams; i++ {
		if err := fwd.HandleRawUDP(ctx, testClientIP, net.ParseIP("192.0.2.1"), testClientPort, 123, []byte("one-way"), replyFn); err != nil {
			t.Fatalf("HandleRawUDP failed: %v", err)
		}
	}
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Errorf("%d one-way datagrams took %v; forwarding must not block on receive", datagrams, elapsed)
	}

	for i := 0; i < datagrams; i++ {
		server.nextDatagram(t)
	}
}

func TestRawUDPForwarder_DeadRelayEvicted(t *testing.T) {
	server := newMockUDPSOCKS5Server(t)
	fwd := newTestRawUDPForwarder(t, server)
	replies, replyFn := newReplyCollector()

	ctx := context.Background()
	if err := fwd.HandleRawUDP(ctx, testClientIP, net.ParseIP("8.8.8.8"), testClientPort, 53, []byte("query-1"), replyFn); err != nil {
		t.Fatalf("HandleRawUDP failed: %v", err)
	}
	server.nextDatagram(t)

	if n := server.associateCount(); n != 1 {
		t.Fatalf("associations = %d, want 1", n)
	}

	// The agent dies: the server closes the association's control connection.
	// The relay must be evicted from the pool without waiting for the TTL.
	server.closeControlConns()

	deadline := time.Now().Add(5 * time.Second)
	for {
		fwd.relayPool.mu.Lock()
		remaining := len(fwd.relayPool.entries)
		fwd.relayPool.mu.Unlock()
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("dead relay was not evicted after control connection closed")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The next packet must create a fresh association and be delivered.
	if err := fwd.HandleRawUDP(ctx, testClientIP, net.ParseIP("8.8.8.8"), testClientPort, 53, []byte("query-2"), replyFn); err != nil {
		t.Fatalf("HandleRawUDP after eviction failed: %v", err)
	}

	if n := server.associateCount(); n != 2 {
		t.Fatalf("associations = %d, want 2 (fresh association after eviction)", n)
	}

	dg := server.nextDatagram(t)
	if string(dg.payload) != "query-2" {
		t.Fatalf("payload = %q, want %q", dg.payload, "query-2")
	}

	// The fresh association must be fully functional.
	server.respond(t, dg.clientAddr, "8.8.8.8", 53, []byte("answer-2"))
	r := nextReply(t, replies)
	if string(r.payload) != "answer-2" {
		t.Errorf("payload = %q, want %q", r.payload, "answer-2")
	}
}

func TestRawUDPForwarder_NoClient(t *testing.T) {
	fwd := NewRawUDPForwarder(nil, zap.NewNop())
	defer fwd.Close()

	err := fwd.HandleRawUDP(context.Background(), testClientIP, net.ParseIP("8.8.8.8"), testClientPort, 53, []byte("x"), nil)
	if err == nil {
		t.Error("expected error when no SOCKS5 client is configured")
	}
}

func TestRelayPool_Remove(t *testing.T) {
	logger := zap.NewNop()
	pool := NewRelayPool(time.Minute, logger)
	defer pool.Close()

	ctx := context.Background()
	createFunc := func(ctx context.Context) (*socks5.UDPRelay, error) {
		return nil, nil
	}

	_, _ = pool.GetOrCreate(ctx, "key", createFunc)

	// Removing with a non-matching relay instance must be a no-op.
	pool.Remove("key", &socks5.UDPRelay{})
	pool.mu.Lock()
	_, exists := pool.entries["key"]
	pool.mu.Unlock()
	if !exists {
		t.Error("entry must survive Remove with a different relay instance")
	}

	// Removing with the pooled instance evicts it.
	relay, _ := pool.GetOrCreate(ctx, "key", createFunc)
	pool.Remove("key", relay)
	pool.mu.Lock()
	_, exists = pool.entries["key"]
	pool.mu.Unlock()
	if exists {
		t.Error("entry should be evicted by Remove with the pooled relay")
	}
}
