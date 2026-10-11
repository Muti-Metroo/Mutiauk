package stack

import (
	"net"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// recordingUDPHandler records intercepted UDP packets.
type recordingUDPHandler struct {
	mu     sync.Mutex
	srcIPs []net.IP
	dstIPs []net.IP
	ports  [][2]uint16
}

func (h *recordingUDPHandler) HandleUDPPacket(srcIP, dstIP net.IP, srcPort, dstPort uint16, payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.srcIPs = append(h.srcIPs, srcIP)
	h.dstIPs = append(h.dstIPs, dstIP)
	h.ports = append(h.ports, [2]uint16{srcPort, dstPort})
}

func (h *recordingUDPHandler) calls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.srcIPs)
}

// recordingICMPHandler records intercepted ICMP echo requests.
type recordingICMPHandler struct {
	mu    sync.Mutex
	count int
}

func (h *recordingICMPHandler) HandleICMPPacket(srcIP, dstIP net.IP, id, seq uint16, payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.count++
}

// recordingDispatcher counts packets that fall through to gVisor.
type recordingDispatcher struct {
	stack.NetworkDispatcher
	mu    sync.Mutex
	count int
}

func (r *recordingDispatcher) DeliverNetworkPacket(protocol tcpip.NetworkProtocolNumber, pkt *stack.PacketBuffer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.count++
}

func (r *recordingDispatcher) packets() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

type interceptTestEnv struct {
	dispatcher *interceptDispatcher
	udp        *recordingUDPHandler
	icmp       *recordingICMPHandler
	fallback   *recordingDispatcher
}

func newInterceptTestEnv() *interceptTestEnv {
	udp := &recordingUDPHandler{}
	icmp := &recordingICMPHandler{}
	fallback := &recordingDispatcher{}
	ep := newInterceptEndpoint(nil, udp, icmp, nil, zap.NewNop())
	return &interceptTestEnv{
		dispatcher: &interceptDispatcher{NetworkDispatcher: fallback, ep: ep},
		udp:        udp,
		icmp:       icmp,
		fallback:   fallback,
	}
}

// deliverIPv4 feeds a raw IPv4 packet through the intercept dispatcher on the
// unparsed-header path (the path taken for packets read from the TUN device).
func (e *interceptTestEnv) deliverIPv4(data []byte) {
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(data),
	})
	defer pkt.DecRef()
	e.dispatcher.DeliverNetworkPacket(header.IPv4ProtocolNumber, pkt)
}

// buildIPv4UDPPacket builds an IPv4 UDP packet with the given flags and
// fragment offset.
func buildIPv4UDPPacket(srcIP, dstIP net.IP, srcPort, dstPort uint16, flags uint8, fragOffset uint16, payload []byte) []byte {
	ipLen := header.IPv4MinimumSize
	totalLen := ipLen + header.UDPMinimumSize + len(payload)
	pkt := make([]byte, totalLen)

	ip := header.IPv4(pkt)
	ip.Encode(&header.IPv4Fields{
		TotalLength: uint16(totalLen),
		TTL:         64,
		Protocol:    uint8(header.UDPProtocolNumber),
		SrcAddr:     tcpip.AddrFrom4Slice(srcIP.To4()),
		DstAddr:     tcpip.AddrFrom4Slice(dstIP.To4()),
	})
	ip.SetFlagsFragmentOffset(flags, fragOffset)
	ip.SetChecksum(^ip.CalculateChecksum())

	udp := header.UDP(pkt[ipLen:])
	udp.Encode(&header.UDPFields{
		SrcPort: srcPort,
		DstPort: dstPort,
		Length:  uint16(header.UDPMinimumSize + len(payload)),
	})
	copy(pkt[ipLen+header.UDPMinimumSize:], payload)

	return pkt
}

// buildIPv4ICMPPacket builds an IPv4 packet carrying protocol ICMPv4 with the
// given flags, fragment offset, and body (the body is only a well-formed ICMP
// echo request for offset 0).
func buildIPv4ICMPPacket(srcIP, dstIP net.IP, flags uint8, fragOffset uint16, body []byte) []byte {
	ipLen := header.IPv4MinimumSize
	totalLen := ipLen + len(body)
	pkt := make([]byte, totalLen)

	ip := header.IPv4(pkt)
	ip.Encode(&header.IPv4Fields{
		TotalLength: uint16(totalLen),
		TTL:         64,
		Protocol:    uint8(header.ICMPv4ProtocolNumber),
		SrcAddr:     tcpip.AddrFrom4Slice(srcIP.To4()),
		DstAddr:     tcpip.AddrFrom4Slice(dstIP.To4()),
	})
	ip.SetFlagsFragmentOffset(flags, fragOffset)
	ip.SetChecksum(^ip.CalculateChecksum())
	copy(pkt[ipLen:], body)

	return pkt
}

func TestIntercept_UnfragmentedUDPIntercepted(t *testing.T) {
	env := newInterceptTestEnv()

	srcIP := net.ParseIP("10.200.200.2")
	dstIP := net.ParseIP("8.8.8.8")
	pkt := buildIPv4UDPPacket(srcIP, dstIP, 5300, 53, 0, 0, []byte("dns query"))

	env.deliverIPv4(pkt)

	if env.udp.calls() != 1 {
		t.Fatalf("expected UDP handler to be called once, got %d", env.udp.calls())
	}
	if env.udp.ports[0] != [2]uint16{5300, 53} {
		t.Errorf("ports = %v, want [5300 53]", env.udp.ports[0])
	}
}

func TestIntercept_FirstFragmentNotIntercepted(t *testing.T) {
	env := newInterceptTestEnv()

	srcIP := net.ParseIP("10.200.200.2")
	dstIP := net.ParseIP("8.8.8.8")
	// First fragment: offset 0 but MF set, so the UDP header is present yet
	// the datagram is incomplete.
	pkt := buildIPv4UDPPacket(srcIP, dstIP, 5300, 53, header.IPv4FlagMoreFragments, 0, []byte("partial dns query"))

	env.deliverIPv4(pkt)

	if env.udp.calls() != 0 {
		t.Error("first fragment must not be intercepted as a complete UDP packet")
	}
	if env.fallback.packets() != 0 {
		t.Error("fragment must be dropped, not passed to gVisor")
	}
}

func TestIntercept_NonFirstFragmentNotIntercepted(t *testing.T) {
	env := newInterceptTestEnv()

	srcIP := net.ParseIP("10.200.200.2")
	dstIP := net.ParseIP("8.8.8.8")
	// Non-first fragment: offset != 0. The payload is not a UDP header; the
	// first bytes deliberately decode to plausible port numbers to prove they
	// are not misread (0x1435 = 5173, 0x0035 = 53).
	payload := append([]byte{0x14, 0x35, 0x00, 0x35, 0x00, 0x10, 0x00, 0x00}, []byte("fragment body")...)
	pkt := buildIPv4UDPPacket(srcIP, dstIP, 0, 0, 0, 185, payload)

	env.deliverIPv4(pkt)

	if env.udp.calls() != 0 {
		t.Error("non-first fragment must not be intercepted (payload bytes are not a UDP header)")
	}
	if env.fallback.packets() != 0 {
		t.Error("fragment must be dropped, not passed to gVisor")
	}
}

func TestIntercept_FragmentedICMPNotIntercepted(t *testing.T) {
	env := newInterceptTestEnv()

	srcIP := net.ParseIP("10.200.200.2")
	dstIP := net.ParseIP("8.8.8.8")
	// Non-first ICMP fragment whose payload starts with byte 8 (echo request
	// type) - this must not be misread as an ICMP echo request header.
	body := append([]byte{8, 0, 0, 0, 0, 1, 0, 1}, []byte("icmp fragment body")...)
	pkt := buildIPv4ICMPPacket(srcIP, dstIP, 0, 185, body)

	env.deliverIPv4(pkt)

	if env.icmp.count != 0 {
		t.Error("ICMP fragment must not be intercepted as an echo request")
	}
	if env.fallback.packets() != 0 {
		t.Error("fragment must be dropped, not passed to gVisor")
	}
}

func TestInterceptFragmentDrop_RateLimitedLog(t *testing.T) {
	limiter := newLogRateLimiter(time.Minute)

	suppressed, ok := limiter.allow()
	if !ok {
		t.Fatal("first event should be allowed")
	}
	if suppressed != 0 {
		t.Errorf("suppressed = %d, want 0", suppressed)
	}

	for i := 0; i < 5; i++ {
		if _, ok := limiter.allow(); ok {
			t.Fatal("events within the interval should be suppressed")
		}
	}
}
