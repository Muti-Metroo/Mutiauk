//go:build windows

package stack

import (
	"context"

	"github.com/Muti-Metroo/Mutiauk/internal/tun"
	"go.uber.org/zap"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// channelQueueLen bounds the channel endpoint's outbound queue. Packets
// beyond this are dropped by gVisor (TCP retransmits, UDP/ICMP are
// best-effort), which is the desired backpressure behaviour.
const channelQueueLen = 512

// newBaseLinkEndpoint returns a gVisor channel endpoint plus a pump.
// Windows has no fdbased equivalent (it is Linux-only), so we drive the
// link layer ourselves: the pump copies packets between the Wintun
// device and the channel. Wintun delivers raw IP packets, so -- unlike
// the macOS pump -- there is no address-family header to add or strip.
func newBaseLinkEndpoint(tunDev tun.Device, mtu int, logger *zap.Logger) (stack.LinkEndpoint, linkStarter, error) {
	ep := channel.New(channelQueueLen, uint32(mtu), "")
	return ep, &windowsPump{ch: ep, dev: tunDev, mtu: mtu, logger: logger}, nil
}

// windowsPump moves packets between a Wintun device and a gVisor channel
// endpoint.
type windowsPump struct {
	ch     *channel.Endpoint
	dev    tun.Device
	mtu    int
	logger *zap.Logger
}

func (p *windowsPump) start(ctx context.Context) {
	go p.inbound(ctx)
	go p.outbound(ctx)
}

// inbound reads IP packets from the Wintun device and injects them into
// the stack. InjectInbound delivers to the channel's network dispatcher
// (the intercept dispatcher), so UDP/ICMP interception applies just as it
// does on the Linux fdbased path.
func (p *windowsPump) inbound(ctx context.Context) {
	buf := make([]byte, p.mtu)
	for {
		n, err := p.dev.Read(buf)
		if err != nil {
			if ctx.Err() == nil {
				p.logger.Debug("wintun read ended", zap.Error(err))
			}
			return
		}
		if n == 0 {
			continue
		}
		proto := protocolNumber(buf[0])
		if proto == 0 {
			continue
		}
		// Copy: the packet buffer outlives this iteration's reuse of buf.
		data := make([]byte, n)
		copy(data, buf[:n])
		pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: buffer.MakeWithData(data),
		})
		p.ch.InjectInbound(proto, pkt)
		pkt.DecRef()
	}
}

// outbound reads packets the stack wants to transmit and writes them to
// the Wintun device.
func (p *windowsPump) outbound(ctx context.Context) {
	for {
		pkt := p.ch.ReadContext(ctx)
		if pkt == nil {
			return // context cancelled
		}
		var out []byte
		for _, v := range pkt.AsSlices() {
			out = append(out, v...)
		}
		pkt.DecRef()
		if _, err := p.dev.Write(out); err != nil {
			p.logger.Debug("wintun write failed", zap.Error(err))
		}
	}
}

// protocolNumber maps the leading IP-version nibble to a gVisor network
// protocol number, or 0 if it is neither IPv4 nor IPv6.
func protocolNumber(first byte) tcpip.NetworkProtocolNumber {
	switch first >> 4 {
	case 4:
		return header.IPv4ProtocolNumber
	case 6:
		return header.IPv6ProtocolNumber
	default:
		return 0
	}
}
