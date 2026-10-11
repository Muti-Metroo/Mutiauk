//go:build darwin

package stack

import (
	"context"

	"github.com/postalsys/mutiauk/internal/tun"
	"go.uber.org/zap"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

const (
	// channelQueueLen bounds the channel endpoint's outbound queue. Packets
	// beyond this are dropped by gVisor (TCP retransmits, UDP/ICMP are
	// best-effort), which is the desired backpressure behaviour.
	channelQueueLen = 512

	// afHeaderSlack is extra read headroom above the MTU. The device already
	// strips the 4-byte BSD address-family header, so this is only a margin.
	afHeaderSlack = 4
)

// newBaseLinkEndpoint returns a gVisor channel endpoint plus a pump. macOS has
// no fdbased equivalent (it is Linux-only), so we drive the link layer
// ourselves: the pump copies packets between the utun device and the channel.
func newBaseLinkEndpoint(tunDev tun.Device, mtu int, logger *zap.Logger) (stack.LinkEndpoint, linkStarter, error) {
	ep := channel.New(channelQueueLen, uint32(mtu), "")
	return ep, &darwinPump{ch: ep, dev: tunDev, mtu: mtu, logger: logger}, nil
}

// darwinPump moves packets between a utun device and a gVisor channel endpoint.
// The TUN device adds and strips the 4-byte BSD address-family header, so the
// pump deals only in raw IP packets.
type darwinPump struct {
	ch     *channel.Endpoint
	dev    tun.Device
	mtu    int
	logger *zap.Logger
}

func (p *darwinPump) start(ctx context.Context) {
	go p.inbound(ctx)
	go p.outbound(ctx)
}

// inbound reads IP packets from the utun device and injects them into the
// stack. InjectInbound delivers to the channel's network dispatcher, which is
// the intercept dispatcher, so UDP/ICMP interception applies just as it does
// on the Linux fdbased path.
func (p *darwinPump) inbound(ctx context.Context) {
	for {
		// One allocation per packet, owned by the PacketBuffer: reading into a
		// reused buffer and then copying would add a second copy, and the
		// packet outlives this iteration, so reuse is not safe here.
		buf := make([]byte, p.mtu+afHeaderSlack)
		n, err := p.dev.Read(buf)
		if err != nil {
			if ctx.Err() == nil {
				p.logger.Debug("utun read ended", zap.Error(err))
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
		pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: buffer.MakeWithData(buf[:n]),
		})
		p.ch.InjectInbound(proto, pkt)
		pkt.DecRef()
	}
}

// outbound reads packets the stack wants to transmit and writes them to the
// utun device (which prepends the address-family header). The flatten buffer
// is reused across packets: the device copies it into its own framed write
// buffer before returning.
func (p *darwinPump) outbound(ctx context.Context) {
	var out []byte
	for {
		pkt := p.ch.ReadContext(ctx)
		if pkt == nil {
			return // context cancelled
		}
		out = out[:0]
		for _, v := range pkt.AsSlices() {
			out = append(out, v...)
		}
		pkt.DecRef()
		if _, err := p.dev.Write(out); err != nil {
			p.logger.Debug("utun write failed", zap.Error(err))
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
