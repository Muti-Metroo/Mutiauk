package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/postalsys/mutiauk/internal/socks5"
	"go.uber.org/zap"
)

// ICMP Destination Unreachable codes (RFC 792 / RFC 4443) used when an echo
// cannot be forwarded through the mesh. Kept as local constants so this package
// need not depend on the gVisor header package.
const (
	icmpv4NetUnreachable  = 0
	icmpv4HostUnreachable = 1
	icmpv4AdminProhibited = 13

	icmpv6NoRoute            = 0
	icmpv6AdminProhibited    = 1
	icmpv6AddressUnreachable = 3
)

// ICMPUnreachableError indicates an intercepted ICMP echo could not be
// forwarded through the mesh (the session could not be established). Callers
// that hold the local sender's link can reply with an ICMP Destination
// Unreachable carrying the embedded codes so the sender fails fast instead of
// timing out. It is returned only for forwarding-setup failures, never for a
// destination that is reachable but does not answer.
type ICMPUnreachableError struct {
	Reason string
	V4Code byte
	V6Code byte
}

func (e *ICMPUnreachableError) Error() string { return e.Reason }

// ICMPUnreachableCodes returns the ICMPv4 and ICMPv6 Destination Unreachable
// codes that best describe this failure. The stack detects this method via an
// interface assertion to decide what error packet to synthesize.
func (e *ICMPUnreachableError) ICMPUnreachableCodes() (v4Code, v6Code byte) {
	return e.V4Code, e.V6Code
}

// mapAssociateError converts an ICMP-session setup failure into an
// ICMPUnreachableError, mapping the SOCKS5 reply code (when present) to the
// closest ICMP Destination Unreachable code.
func mapAssociateError(err error) error {
	v4, v6 := byte(icmpv4HostUnreachable), byte(icmpv6AddressUnreachable)

	var se *socks5.Error
	if errors.As(err, &se) {
		switch se.Code {
		case socks5.ReplyNotAllowed, socks5.ReplyCommandNotSupported:
			// Exit refuses ICMP (disabled / not supported) -> admin prohibited.
			v4, v6 = icmpv4AdminProhibited, icmpv6AdminProhibited
		case socks5.ReplyNetworkUnreachable:
			v4, v6 = icmpv4NetUnreachable, icmpv6NoRoute
		case socks5.ReplyHostUnreachable:
			v4, v6 = icmpv4HostUnreachable, icmpv6AddressUnreachable
		}
	}

	return &ICMPUnreachableError{Reason: err.Error(), V4Code: v4, V6Code: v6}
}

// ICMPForwarder forwards ICMP echo requests through a SOCKS5 proxy.
type ICMPForwarder struct {
	clientHolder
	logger  *zap.Logger
	timeout time.Duration

	// Session cache
	mu       sync.RWMutex
	sessions map[icmpSessionKey]*icmpSession

	// Shutdown
	done      chan struct{}
	closeOnce sync.Once
}

// icmpSessionKey uniquely identifies an ICMP session.
type icmpSessionKey struct {
	dstIP string
	id    uint16
}

// icmpSession represents an active ICMP session through SOCKS5.
type icmpSession struct {
	relay    *socks5.ICMPRelay
	lastUsed time.Time
	mu       sync.Mutex
}

// NewICMPForwarder creates a new ICMP forwarder.
func NewICMPForwarder(client *socks5.Client, logger *zap.Logger, timeout time.Duration) *ICMPForwarder {
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	f := &ICMPForwarder{
		clientHolder: clientHolder{client: client},
		logger:       logger,
		timeout:      timeout,
		sessions:     make(map[icmpSessionKey]*icmpSession),
		done:         make(chan struct{}),
	}

	// Start cleanup goroutine
	go f.cleanupLoop()

	return f
}

// HandleICMP implements stack.ICMPHandler.
// Forwards an ICMP echo request through the SOCKS5 proxy and returns the reply.
func (f *ICMPForwarder) HandleICMP(ctx context.Context, srcIP, dstIP net.IP, id, seq uint16, payload []byte) ([]byte, error) {
	f.logger.Debug("forwarding ICMP echo request",
		zap.String("src", srcIP.String()),
		zap.String("dst", dstIP.String()),
		zap.Uint16("id", id),
		zap.Uint16("seq", seq),
		zap.Int("payload_len", len(payload)),
	)

	// Get or create session. A setup failure (no route, exit can't open an
	// ICMP socket, ICMP disabled) is mapped to an ICMPUnreachableError so the
	// stack can reply to the local sender instead of letting it time out.
	session, err := f.getOrCreateSession(ctx, dstIP, id)
	if err != nil {
		return nil, mapAssociateError(err)
	}

	// Send echo request and wait for reply
	replyPayload, err := session.sendEcho(ctx, id, seq, payload, f.timeout)
	if err != nil {
		return nil, fmt.Errorf("ICMP echo failed: %w", err)
	}

	return replyPayload, nil
}

// getOrCreateSession gets an existing session or creates a new one.
// The SOCKS5 ICMP ASSOCIATE handshake (network I/O, potentially seconds on
// a slow mesh) runs outside the lock, mirroring RelayPool.GetOrCreate, so
// session setup never stalls the RLock fast path for other sessions.
func (f *ICMPForwarder) getOrCreateSession(ctx context.Context, dstIP net.IP, id uint16) (*icmpSession, error) {
	key := icmpSessionKey{dstIP: dstIP.String(), id: id}

	// Fast path: existing session
	f.mu.RLock()
	session, exists := f.sessions[key]
	f.mu.RUnlock()

	if exists {
		return session, nil
	}

	// Slow path: create the relay outside the lock (network I/O)
	client := f.clientHolder.Get()
	relay, err := client.ICMPAssociate(ctx, dstIP)
	if err != nil {
		return nil, err
	}

	// Re-acquire the lock and double-check: another goroutine may have
	// installed a session for this key while we were doing I/O.
	f.mu.Lock()
	defer f.mu.Unlock()

	if session, exists = f.sessions[key]; exists {
		relay.Close()
		return session, nil
	}

	session = &icmpSession{
		relay:    relay,
		lastUsed: time.Now(),
	}
	f.sessions[key] = session

	f.logger.Debug("created ICMP session",
		zap.String("dst", dstIP.String()),
		zap.Uint16("id", id),
	)

	return session, nil
}

// sendEcho sends an echo request and waits for the reply.
func (s *icmpSession) sendEcho(ctx context.Context, id, seq uint16, payload []byte, timeout time.Duration) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastUsed = time.Now()

	// Send echo request
	if err := s.relay.SendEcho(id, seq, payload); err != nil {
		return nil, err
	}

	// Wait for reply with timeout
	if timeout > 0 {
		s.relay.SetReadDeadline(time.Now().Add(timeout))
	}

	// Read reply
	replyPayload, replySeq, err := s.relay.ReceiveEcho()
	if err != nil {
		return nil, err
	}

	// Verify sequence matches (id is always matched by the server)
	if replySeq != seq {
		return nil, fmt.Errorf("sequence mismatch: expected %d, got %d", seq, replySeq)
	}

	return replyPayload, nil
}

// cleanupLoop periodically removes stale sessions.
func (f *ICMPForwarder) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-f.done:
			return
		case <-ticker.C:
			f.cleanup()
		}
	}
}

// cleanup removes sessions that haven't been used recently.
func (f *ICMPForwarder) cleanup() {
	f.mu.Lock()
	defer f.mu.Unlock()

	now := time.Now()
	staleThreshold := 60 * time.Second

	for key, session := range f.sessions {
		session.mu.Lock()
		if now.Sub(session.lastUsed) > staleThreshold {
			session.relay.Close()
			delete(f.sessions, key)
			f.logger.Debug("removed stale ICMP session",
				zap.String("dst", key.dstIP),
				zap.Uint16("id", key.id),
			)
		}
		session.mu.Unlock()
	}
}

// clearSessions closes all sessions and removes them from the map.
// Caller must hold f.mu.
func (f *ICMPForwarder) clearSessions() {
	for key, session := range f.sessions {
		session.relay.Close()
		delete(f.sessions, key)
	}
}

// Close closes all sessions and stops the forwarder. Safe to call multiple times.
func (f *ICMPForwarder) Close() error {
	f.closeOnce.Do(func() {
		close(f.done)
	})

	f.mu.Lock()
	defer f.mu.Unlock()

	f.clearSessions()

	return nil
}

// UpdateClient updates the SOCKS5 client and clears existing sessions.
func (f *ICMPForwarder) UpdateClient(client *socks5.Client) {
	f.clientHolder.Set(client)

	f.mu.Lock()
	defer f.mu.Unlock()

	f.clearSessions()
}
