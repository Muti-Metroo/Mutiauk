package stack

import (
	"errors"
	"net"
	"testing"

	"gvisor.dev/gvisor/pkg/tcpip/header"
)

// stubUnreachable is an error carrying ICMP unreachable codes, mirroring what
// proxy.ICMPUnreachableError provides, so the stack helper can be tested in
// isolation from the proxy package.
type stubUnreachable struct {
	v4, v6 byte
}

func (e *stubUnreachable) Error() string                      { return "stub unreachable" }
func (e *stubUnreachable) ICMPUnreachableCodes() (byte, byte) { return e.v4, e.v6 }

func TestBuildICMPUnreachableForError_PlainErrorReturnsNil(t *testing.T) {
	pkt := buildICMPUnreachableForError(
		net.ParseIP("10.0.0.1"), net.ParseIP("8.8.8.8"), 1, 2, []byte("data"),
		errors.New("read timeout"),
	)
	if pkt != nil {
		t.Errorf("plain (non-unreachable) error should not synthesize a packet, got %d bytes", len(pkt))
	}
}

func TestBuildICMPv4DestUnreachable(t *testing.T) {
	src := net.ParseIP("10.0.0.1").To4() // local sender
	dst := net.ParseIP("8.8.8.8").To4()  // ping target
	const id, seq = 0x1234, 0x0007
	payload := []byte("muti-metroo ping payload")

	pkt := buildICMPUnreachableForError(src, dst, id, seq, payload, &stubUnreachable{v4: byte(header.ICMPv4AdminProhibited)})
	if pkt == nil {
		t.Fatal("expected a packet")
	}

	ip := header.IPv4(pkt)
	srcAddr := ip.SourceAddress()
	dstAddr := ip.DestinationAddress()
	if got := net.IP(srcAddr.AsSlice()).To4(); !got.Equal(dst) {
		t.Errorf("error packet src = %v, want target %v", got, dst)
	}
	if got := net.IP(dstAddr.AsSlice()).To4(); !got.Equal(src) {
		t.Errorf("error packet dst = %v, want sender %v", got, src)
	}
	if ip.Protocol() != uint8(header.ICMPv4ProtocolNumber) {
		t.Fatalf("protocol = %d, want ICMP", ip.Protocol())
	}

	icmp := header.ICMPv4(pkt[ip.HeaderLength():])
	if icmp.Type() != header.ICMPv4DstUnreachable {
		t.Errorf("type = %d, want DstUnreachable(3)", icmp.Type())
	}
	if icmp.Code() != header.ICMPv4AdminProhibited {
		t.Errorf("code = %d, want AdminProhibited(13)", icmp.Code())
	}

	// The embedded original datagram (after the 8-byte ICMP header) must be the
	// reconstructed echo request: IPv4 header + echo with our id/seq.
	embed := pkt[int(ip.HeaderLength())+header.ICMPv4MinimumSize:]
	if len(embed) < header.IPv4MinimumSize+8 {
		t.Fatalf("embedded datagram too short: %d", len(embed))
	}
	oIP := header.IPv4(embed)
	oICMP := header.ICMPv4(embed[oIP.HeaderLength():])
	if oICMP.Type() != header.ICMPv4Echo {
		t.Errorf("embedded type = %d, want Echo(8)", oICMP.Type())
	}
	if oICMP.Ident() != id || oICMP.Sequence() != seq {
		t.Errorf("embedded id/seq = %d/%d, want %d/%d", oICMP.Ident(), oICMP.Sequence(), id, seq)
	}
}

func TestBuildICMPv6DestUnreachable(t *testing.T) {
	src := net.ParseIP("fd00::1") // local sender
	dst := net.ParseIP("2001:4860:4860::8888")
	const id, seq = 0xabcd, 0x0003
	payload := []byte("muti-metroo ping v6")

	pkt := buildICMPUnreachableForError(src, dst, id, seq, payload, &stubUnreachable{v6: byte(header.ICMPv6AddressUnreachable)})
	if pkt == nil {
		t.Fatal("expected a packet")
	}

	ip := header.IPv6(pkt)
	srcAddr := ip.SourceAddress()
	dstAddr := ip.DestinationAddress()
	if got := net.IP(srcAddr.AsSlice()); !got.Equal(dst) {
		t.Errorf("error packet src = %v, want target %v", got, dst)
	}
	if got := net.IP(dstAddr.AsSlice()); !got.Equal(src) {
		t.Errorf("error packet dst = %v, want sender %v", got, src)
	}

	icmp := header.ICMPv6(pkt[header.IPv6MinimumSize:])
	if icmp.Type() != header.ICMPv6DstUnreachable {
		t.Errorf("type = %d, want DstUnreachable(1)", icmp.Type())
	}
	if icmp.Code() != header.ICMPv6AddressUnreachable {
		t.Errorf("code = %d, want AddressUnreachable(3)", icmp.Code())
	}

	// Embedded original ICMPv6 echo request after the 8-byte header.
	embed := pkt[header.IPv6MinimumSize+header.ICMPv6MinimumSize:]
	if len(embed) < header.IPv6MinimumSize+8 {
		t.Fatalf("embedded datagram too short: %d", len(embed))
	}
	oEcho := embed[header.IPv6MinimumSize:]
	gotID := uint16(oEcho[4])<<8 | uint16(oEcho[5])
	gotSeq := uint16(oEcho[6])<<8 | uint16(oEcho[7])
	if gotID != id || gotSeq != seq {
		t.Errorf("embedded id/seq = %d/%d, want %d/%d", gotID, gotSeq, id, seq)
	}
}
