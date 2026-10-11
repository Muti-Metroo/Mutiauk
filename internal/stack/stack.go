package stack

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/postalsys/mutiauk/internal/tun"
	"go.uber.org/zap"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// TCPHandler handles TCP connections
type TCPHandler interface {
	HandleTCP(ctx context.Context, conn net.Conn, srcAddr, dstAddr net.Addr) error
}

// TCPPreConnector can pre-establish connections before TCP handshake completes.
// This enables accurate port scanning by rejecting connections to unreachable
// destinations before the TCP handshake is completed.
type TCPPreConnector interface {
	PreConnect(ctx context.Context, srcAddr, dstAddr net.Addr) (net.Conn, error)
}

// TCPPendingCleaner cleans up pending pre-established connections that were
// never consumed (e.g., when CreateEndpoint fails after a successful PreConnect).
type TCPPendingCleaner interface {
	CleanupPending(srcAddr, dstAddr net.Addr)
}

// UDPReplyFunc delivers a UDP payload back to the local client through the
// TUN device. responderIP/responderPort identify the actual responder (read
// from the relay's datagram header), clientIP/clientPort the original local
// sender. Implementations must be safe to call from any goroutine and must
// not retain payload.
type UDPReplyFunc func(responderIP, clientIP net.IP, responderPort, clientPort uint16, payload []byte)

// RawUDPHandler handles raw UDP packets with payload
type RawUDPHandler interface {
	// HandleRawUDP forwards a UDP packet through the mesh.
	// srcIP/srcPort: original source (client)
	// dstIP/dstPort: destination to forward to
	// payload: UDP payload data
	// reply: callback that delivers response datagrams back to the client;
	// it may be invoked zero or more times, possibly after HandleRawUDP
	// returns, once per received response datagram.
	// Returns an error only if the packet could not be forwarded.
	HandleRawUDP(ctx context.Context, srcIP, dstIP net.IP, srcPort, dstPort uint16, payload []byte, reply UDPReplyFunc) error
}

// ICMPHandler handles ICMP echo requests
type ICMPHandler interface {
	// HandleICMP sends an ICMP echo request through the proxy and returns the reply.
	// srcIP: original source (client)
	// dstIP: destination to ping
	// id: ICMP identifier
	// seq: ICMP sequence number
	// payload: ICMP payload data
	// Returns the reply payload or error
	HandleICMP(ctx context.Context, srcIP, dstIP net.IP, id, seq uint16, payload []byte) ([]byte, error)
}

// Config holds stack configuration
type Config struct {
	MTU           int
	IPv4Addr      net.IP
	IPv4Mask      net.IPMask
	IPv6Addr      net.IP
	IPv6Mask      net.IPMask
	TCPHandler    TCPHandler
	RawUDPHandler RawUDPHandler // Handler for link-layer intercepted UDP
	ICMPHandler   ICMPHandler   // ICMP echo handler
	// ICMPErrorReplies, when true, makes HandleICMPPacket reply to the local
	// sender with an ICMP Destination Unreachable when an echo cannot be
	// forwarded (the handler returns an error exposing ICMPUnreachableCodes).
	ICMPErrorReplies bool
	Logger           *zap.Logger
	// MaxTCPInFlight caps concurrent pending TCP connections in the gVisor
	// forwarder. Zero uses defaultMaxTCPInFlight.
	MaxTCPInFlight int
}

const (
	maxConcurrentUDP  = 1024
	maxConcurrentICMP = 256

	// defaultMaxTCPInFlight bounds concurrent pending TCP connections
	// (received SYN, handshake not yet completed). Because PreConnect dials
	// the mesh for every inbound SYN, an unbounded limit lets a port scan
	// amplify into thousands of simultaneous mesh connections and SOCKS5
	// handshakes. Beyond the cap gVisor drops the SYN (counted in
	// TCP.ForwardMaxInFlightDrop), so excess scans time out instead of
	// exhausting the agent; the RST-on-closed-port semantics of PreConnect
	// are unaffected for connections under the cap.
	defaultMaxTCPInFlight = 1024
)

// effectiveMaxTCPInFlight returns the configured in-flight TCP cap, or the
// default when unset.
func (c Config) effectiveMaxTCPInFlight() int {
	if c.MaxTCPInFlight > 0 {
		return c.MaxTCPInFlight
	}
	return defaultMaxTCPInFlight
}

// linkStarter starts any background work needed to drive the link endpoint.
// On Linux the fdbased endpoint runs its own read loop, so this is a no-op;
// on macOS it starts the goroutines that pump packets to and from the utun
// device.
type linkStarter interface {
	start(ctx context.Context)
}

// Stack wraps gVisor's network stack
type Stack struct {
	stack       *stack.Stack
	tunDev      tun.Device
	linkEP      stack.LinkEndpoint
	interceptEP *interceptEndpoint
	starter     linkStarter
	cfg         Config
	logger      *zap.Logger

	// Concurrency control
	udpSem  chan struct{}  // Limits concurrent UDP goroutines
	icmpSem chan struct{}  // Limits concurrent ICMP goroutines
	tcpWg   sync.WaitGroup // Tracks in-flight TCP handler goroutines

	// runCtx is the daemon-scoped context captured by Run; UDP handler
	// goroutines derive from it so daemon shutdown cancels them.
	runCtxMu sync.RWMutex
	runCtx   context.Context

	// truncLog rate-limits MTU-truncation warnings for UDP responses.
	truncLog *logRateLimiter
}

const (
	nicID = 1
)

// New creates a new network stack
func New(tunDev tun.Device, cfg Config) (*Stack, error) {
	if cfg.MTU == 0 {
		cfg.MTU = 1400
	}

	s := &Stack{
		tunDev:   tunDev,
		cfg:      cfg,
		logger:   cfg.Logger,
		udpSem:   make(chan struct{}, maxConcurrentUDP),
		icmpSem:  make(chan struct{}, maxConcurrentICMP),
		truncLog: newLogRateLimiter(5 * time.Second),
	}

	// Create the gVisor stack
	opts := stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	}

	s.stack = stack.New(opts)

	// Create the platform link endpoint from the TUN device. TUN devices carry
	// raw IP packets (no ethernet header). On Linux this wraps the fd with
	// gVisor's fdbased endpoint; on macOS it returns a channel endpoint plus a
	// pump (started in Run) that copies packets to and from the utun device.
	baseLinkEP, starter, err := newBaseLinkEndpoint(tunDev, cfg.MTU, cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create link endpoint: %w", err)
	}
	s.starter = starter

	// Wrap with intercept endpoint to capture UDP and ICMP packets
	s.interceptEP = newInterceptEndpoint(baseLinkEP, s, s, tunDev.Write, cfg.Logger)
	s.linkEP = s.interceptEP

	// Create NIC with intercept endpoint
	if err := s.stack.CreateNIC(nicID, s.interceptEP); err != nil {
		return nil, fmt.Errorf("failed to create NIC: %s", err)
	}

	// Enable promiscuous mode to accept all packets (not just those for our IP)
	if err := s.stack.SetPromiscuousMode(nicID, true); err != nil {
		return nil, fmt.Errorf("failed to set promiscuous mode: %s", err)
	}

	// Enable spoofing to allow sending packets from any source
	if err := s.stack.SetSpoofing(nicID, true); err != nil {
		return nil, fmt.Errorf("failed to enable spoofing: %s", err)
	}

	// Add IPv4 address
	if cfg.IPv4Addr != nil {
		addr := tcpip.AddrFrom4Slice(cfg.IPv4Addr.To4())
		ones, _ := cfg.IPv4Mask.Size()
		protoAddr := tcpip.ProtocolAddress{
			Protocol:          ipv4.ProtocolNumber,
			AddressWithPrefix: tcpip.AddressWithPrefix{Address: addr, PrefixLen: ones},
		}
		if err := s.stack.AddProtocolAddress(nicID, protoAddr, stack.AddressProperties{}); err != nil {
			return nil, fmt.Errorf("failed to add IPv4 address: %s", err)
		}
	}

	// Add IPv6 address
	if cfg.IPv6Addr != nil {
		addr := tcpip.AddrFrom16Slice(cfg.IPv6Addr.To16())
		ones, _ := cfg.IPv6Mask.Size()
		protoAddr := tcpip.ProtocolAddress{
			Protocol:          ipv6.ProtocolNumber,
			AddressWithPrefix: tcpip.AddressWithPrefix{Address: addr, PrefixLen: ones},
		}
		if err := s.stack.AddProtocolAddress(nicID, protoAddr, stack.AddressProperties{}); err != nil {
			return nil, fmt.Errorf("failed to add IPv6 address: %s", err)
		}
	}

	// Set default routes (catch-all)
	s.stack.SetRouteTable([]tcpip.Route{
		{
			Destination: header.IPv4EmptySubnet,
			NIC:         nicID,
		},
		{
			Destination: header.IPv6EmptySubnet,
			NIC:         nicID,
		},
	})

	// NOTE: Forwarding is intentionally DISABLED
	// When forwarding is enabled, gVisor routes packets directly instead of
	// passing them to transport protocol handlers (TCP/UDP forwarders).
	// With forwarding disabled + promiscuous mode, all packets go to handlers.
	// s.stack.SetForwardingDefaultAndAllNICs(ipv4.ProtocolNumber, true)
	// s.stack.SetForwardingDefaultAndAllNICs(ipv6.ProtocolNumber, true)

	return s, nil
}

// Run starts the network stack and handles connections
func (s *Stack) Run(ctx context.Context) error {
	s.runCtxMu.Lock()
	s.runCtx = ctx
	s.runCtxMu.Unlock()

	// Set up TCP forwarder. The in-flight cap bounds the number of
	// concurrent PreConnect mesh dials a SYN flood can trigger.
	tcpForwarder := tcp.NewForwarder(s.stack, 0, s.cfg.effectiveMaxTCPInFlight(), func(r *tcp.ForwarderRequest) {
		// The forwarder callback runs on a gVisor-internal goroutine; a
		// panic here (e.g. in PreConnect) must not crash the daemon.
		defer s.recoverPanic("TCP forwarder request")
		s.handleTCPForward(ctx, r)
	})
	s.stack.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpForwarder.HandlePacket)

	// UDP is now handled by the intercept endpoint, not gVisor's forwarder
	// The interceptDispatcher catches all UDP packets at the link layer
	// and calls Stack.HandleUDPPacket directly

	// Start the link pump if the platform needs one (macOS). No-op on Linux.
	if s.starter != nil {
		s.starter.start(ctx)
	}

	s.logger.Info("network stack started")

	// Wait for context cancellation
	<-ctx.Done()

	return ctx.Err()
}

// HandleUDPPacket implements UDPPacketHandler for the intercept endpoint.
// Response datagrams arrive asynchronously on the relay association and are
// delivered back to the client through writeUDPResponse.
func (s *Stack) HandleUDPPacket(srcIP, dstIP net.IP, srcPort, dstPort uint16, payload []byte) {
	if s.logger.Core().Enabled(zap.DebugLevel) {
		s.logger.Debug("UDP packet received for forwarding",
			zap.String("src", fmt.Sprintf("%s:%d", srcIP, srcPort)),
			zap.String("dst", fmt.Sprintf("%s:%d", dstIP, dstPort)),
			zap.Int("payload_len", len(payload)),
		)
	}

	if s.cfg.RawUDPHandler == nil {
		s.logger.Debug("no RawUDPHandler configured, dropping UDP packet")
		return
	}

	// Rate-limit concurrent UDP goroutines
	select {
	case s.udpSem <- struct{}{}:
	default:
		if s.logger.Core().Enabled(zap.DebugLevel) {
			s.logger.Debug("UDP semaphore full, dropping packet",
				zap.String("dst", fmt.Sprintf("%s:%d", dstIP, dstPort)),
			)
		}
		return
	}

	// Forward through SOCKS5 in a goroutine to not block the dispatcher.
	// HandleRawUDP returns once the datagram is sent; it never blocks waiting
	// for a response.
	go func() {
		defer s.recoverPanic("UDP packet handler")
		defer func() { <-s.udpSem }()

		ctx, cancel := context.WithTimeout(s.runContext(), 30*time.Second)
		defer cancel()

		if err := s.cfg.RawUDPHandler.HandleRawUDP(ctx, srcIP, dstIP, srcPort, dstPort, payload, s.writeUDPResponse); err != nil {
			if s.logger.Core().Enabled(zap.DebugLevel) {
				s.logger.Debug("UDP forward failed",
					zap.String("dst", fmt.Sprintf("%s:%d", dstIP, dstPort)),
					zap.Error(err),
				)
			}
		}
	}()
}

// recoverPanic confines a panic to the goroutine that raised it: it logs
// the panic with a stack trace instead of letting it crash the whole
// daemon. Deferred at every per-packet and per-connection goroutine entry
// point.
func (s *Stack) recoverPanic(scope string) {
	if r := recover(); r != nil {
		s.logger.Error("panic in "+scope+"; tearing down only this flow",
			zap.Any("panic", r),
			zap.Stack("stacktrace"),
		)
	}
}

// runContext returns the daemon-scoped context captured by Run, falling back
// to context.Background() when Run has not started yet.
func (s *Stack) runContext() context.Context {
	s.runCtxMu.RLock()
	defer s.runCtxMu.RUnlock()
	if s.runCtx != nil {
		return s.runCtx
	}
	return context.Background()
}

// writeUDPResponse synthesizes a UDP packet from responderIP:responderPort to
// clientIP:clientPort and writes it to the TUN device. It implements
// UDPReplyFunc.
//
// The payload is truncated to fit the link MTU: the TUN write is
// all-or-nothing, so an oversized packet would be dropped at the device
// boundary. Truncation is logged at a bounded rate.
func (s *Stack) writeUDPResponse(responderIP, clientIP net.IP, responderPort, clientPort uint16, payload []byte) {
	payload, truncated := clampUDPPayload(payload, s.cfg.MTU, isIPv6(responderIP))
	if truncated {
		if suppressed, ok := s.truncLog.allow(); ok {
			s.logger.Warn("UDP response exceeds link MTU, truncating payload",
				zap.String("responder", fmt.Sprintf("%s:%d", responderIP, responderPort)),
				zap.String("client", fmt.Sprintf("%s:%d", clientIP, clientPort)),
				zap.Int("mtu", s.cfg.MTU),
				zap.Int("suppressed", suppressed),
			)
		}
	}

	responsePkt := BuildUDPResponse(responderIP, clientIP, responderPort, clientPort, payload)

	if _, err := s.tunDev.Write(responsePkt); err != nil {
		s.logger.Error("failed to write UDP response to TUN",
			zap.Error(err),
		)
		return
	}
	s.logger.Debug("UDP response written to TUN",
		zap.Int("len", len(responsePkt)),
	)
}

// clampUDPPayload truncates payload so a synthesized UDP packet carrying it
// fits within mtu. It returns the payload (shared, not copied) and whether
// truncation occurred.
func clampUDPPayload(payload []byte, mtu int, v6 bool) ([]byte, bool) {
	overhead := header.IPv4MinimumSize + header.UDPMinimumSize
	if v6 {
		overhead = header.IPv6MinimumSize + header.UDPMinimumSize
	}
	maxPayload := mtu - overhead
	if maxPayload < 0 {
		maxPayload = 0
	}
	if len(payload) <= maxPayload {
		return payload, false
	}
	return payload[:maxPayload], true
}

// HandleICMPPacket implements ICMPPacketHandler for the intercept endpoint
func (s *Stack) HandleICMPPacket(srcIP, dstIP net.IP, id, seq uint16, payload []byte) {
	if s.logger.Core().Enabled(zap.DebugLevel) {
		s.logger.Debug("ICMP packet received for forwarding",
			zap.String("src", srcIP.String()),
			zap.String("dst", dstIP.String()),
			zap.Uint16("id", id),
			zap.Uint16("seq", seq),
			zap.Int("payload_len", len(payload)),
		)
	}

	if s.cfg.ICMPHandler == nil {
		s.logger.Debug("no ICMPHandler configured, dropping ICMP packet")
		return
	}

	// Rate-limit concurrent ICMP goroutines
	select {
	case s.icmpSem <- struct{}{}:
	default:
		if s.logger.Core().Enabled(zap.DebugLevel) {
			s.logger.Debug("ICMP semaphore full, dropping packet",
				zap.String("dst", dstIP.String()),
			)
		}
		return
	}

	// Forward through SOCKS5 in a goroutine to not block
	go func() {
		defer s.recoverPanic("ICMP packet handler")
		defer func() { <-s.icmpSem }()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		response, err := s.cfg.ICMPHandler.HandleICMP(ctx, srcIP, dstIP, id, seq, payload)
		if err != nil {
			if s.logger.Core().Enabled(zap.DebugLevel) {
				s.logger.Debug("ICMP forward failed",
					zap.String("dst", dstIP.String()),
					zap.Error(err),
				)
			}
			// If forwarding setup failed (vs. the destination simply not
			// answering), reply to the local sender with an ICMP Destination
			// Unreachable so it fails fast instead of timing out.
			if s.cfg.ICMPErrorReplies {
				if errPkt := buildICMPUnreachableForError(srcIP, dstIP, id, seq, payload, err); errPkt != nil {
					if _, werr := s.tunDev.Write(errPkt); werr != nil {
						s.logger.Debug("failed to write ICMP unreachable to TUN", zap.Error(werr))
					} else if s.logger.Core().Enabled(zap.DebugLevel) {
						s.logger.Debug("ICMP unreachable written to TUN",
							zap.String("dst", dstIP.String()))
					}
				}
			}
			return
		}

		if len(response) == 0 {
			return
		}

		// Build ICMP echo reply packet: from dstIP to srcIP
		replyPkt := BuildICMPEchoReply(dstIP, srcIP, id, seq, response)

		// Write response back to TUN
		if _, err := s.tunDev.Write(replyPkt); err != nil {
			s.logger.Error("failed to write ICMP response to TUN",
				zap.Error(err),
			)
		} else {
			s.logger.Debug("ICMP response written to TUN",
				zap.Int("len", len(replyPkt)),
			)
		}
	}()
}

// handleTCPForward handles a forwarded TCP connection
func (s *Stack) handleTCPForward(ctx context.Context, r *tcp.ForwarderRequest) {
	id := r.ID()

	srcAddr := &net.TCPAddr{
		IP:   net.IP(id.RemoteAddress.AsSlice()),
		Port: int(id.RemotePort),
	}
	dstAddr := &net.TCPAddr{
		IP:   net.IP(id.LocalAddress.AsSlice()),
		Port: int(id.LocalPort),
	}

	// If the handler supports pre-connection, try to establish the upstream
	// connection BEFORE completing the TCP handshake. This ensures that
	// port scanning through the proxy is accurate - closed ports will
	// receive RST instead of completing the handshake.
	if preConnector, ok := s.cfg.TCPHandler.(TCPPreConnector); ok {
		_, err := preConnector.PreConnect(ctx, srcAddr, dstAddr)
		if err != nil {
			s.logger.Debug("pre-connect failed, rejecting connection",
				zap.String("src", srcAddr.String()),
				zap.String("dst", dstAddr.String()),
				zap.Error(err),
			)
			// Reject the connection with RST - this makes the port appear closed
			r.Complete(true)
			return
		}
	}

	// Create endpoint (this completes the TCP handshake)
	var wq waiter.Queue
	ep, tcpErr := r.CreateEndpoint(&wq)
	if tcpErr != nil {
		s.logger.Error("failed to create TCP endpoint",
			zap.String("src", fmt.Sprintf("%s:%d", id.RemoteAddress, id.RemotePort)),
			zap.String("dst", fmt.Sprintf("%s:%d", id.LocalAddress, id.LocalPort)),
			zap.String("error", tcpErr.String()),
		)
		// Clean up any pre-established connection that will never be consumed
		if cleaner, ok := s.cfg.TCPHandler.(TCPPendingCleaner); ok {
			cleaner.CleanupPending(srcAddr, dstAddr)
		}
		r.Complete(true)
		return
	}
	r.Complete(false)

	// Convert to net.Conn
	conn := gonet.NewTCPConn(&wq, ep)

	s.logger.Debug("TCP connection",
		zap.String("src", srcAddr.String()),
		zap.String("dst", dstAddr.String()),
	)

	if s.cfg.TCPHandler != nil {
		s.tcpWg.Add(1)
		go func() {
			defer s.recoverPanic("TCP connection handler")
			defer s.tcpWg.Done()
			if err := s.cfg.TCPHandler.HandleTCP(ctx, conn, srcAddr, dstAddr); err != nil {
				s.logger.Debug("TCP handler error",
					zap.String("src", srcAddr.String()),
					zap.String("dst", dstAddr.String()),
					zap.Error(err),
				)
			}
		}()
	} else {
		conn.Close()
	}
}

// Close shuts down the stack and waits for in-flight handlers to finish
func (s *Stack) Close() error {
	if s.stack != nil {
		s.stack.Close()
	}
	// Wait for all in-flight TCP handler goroutines to complete
	s.tcpWg.Wait()
	return nil
}

// Stats returns stack statistics
func (s *Stack) Stats() tcpip.Stats {
	return s.stack.Stats()
}
