package proxy

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/postalsys/mutiauk/internal/socks5"
	"github.com/postalsys/mutiauk/internal/stack"
	"go.uber.org/zap"
)

// udpBufPool reuses 64KB buffers for UDP packet handling to reduce GC pressure.
var udpBufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, DefaultUDPBufferSize)
		return &buf
	},
}

// defaultRelayTTL is how long an idle UDP relay association is kept in the
// pool before the cleanup loop evicts it.
const defaultRelayTTL = 5 * time.Minute

// RawUDPForwarder forwards raw UDP packets through SOCKS5 UDP associations.
//
// Each association (keyed by the local srcIP:srcPort, preserving SOCKS5 UDP
// ASSOCIATE semantics) owns a reader goroutine that continuously drains the
// relay socket, demultiplexes response datagrams by their responder address,
// and delivers them through the reply callback. HandleRawUDP never blocks
// waiting for a response: datagrams are forwarded independently, so
// multi-datagram responses, interleaved destinations, and one-way flows all
// behave like plain UDP.
type RawUDPForwarder struct {
	clientHolder
	logger    *zap.Logger
	relayPool *RelayPool
}

// NewRawUDPForwarder creates a new raw UDP forwarder.
func NewRawUDPForwarder(client *socks5.Client, logger *zap.Logger) *RawUDPForwarder {
	return &RawUDPForwarder{
		clientHolder: clientHolder{client: client},
		logger:       logger,
		relayPool:    NewRelayPool(defaultRelayTTL, logger),
	}
}

// UpdateClient updates the SOCKS5 client and clears the relay pool.
func (f *RawUDPForwarder) UpdateClient(client *socks5.Client) {
	f.clientHolder.Set(client)
	f.relayPool.Clear()
}

// HandleRawUDP forwards a single UDP datagram through the SOCKS5 relay
// association for srcIP:srcPort. It returns once the datagram is sent;
// responses are delivered asynchronously via reply.
func (f *RawUDPForwarder) HandleRawUDP(ctx context.Context, srcIP, dstIP net.IP, srcPort, dstPort uint16, payload []byte, reply stack.UDPReplyFunc) error {
	client := f.clientHolder.Get()
	if client == nil {
		return fmt.Errorf("no SOCKS5 client configured")
	}

	key := addrPortFromIP(srcIP, srcPort)
	relay, err := f.relayPool.GetOrCreate(ctx, key, func(ctx context.Context) (*socks5.UDPRelay, error) {
		r, err := client.UDPAssociate(ctx, nil)
		if err != nil {
			return nil, err
		}
		f.logger.Debug("created new UDP relay",
			zap.String("key", key.String()),
			zap.String("local", r.LocalAddr().String()),
			zap.String("relay_addr", r.RemoteAddr()),
		)
		f.startRelayReader(key, r, srcIP, srcPort, reply)
		// The SOCKS5 server closes the control connection when the UDP
		// association dies (e.g. agent restart); evict the dead relay so the
		// next packet creates a fresh association instead of sending into
		// the void until the TTL expires.
		r.MonitorControlConn(func() {
			f.logger.Debug("UDP relay control connection closed, evicting relay",
				zap.String("key", key.String()),
			)
			f.relayPool.Remove(key, r)
		})
		return r, nil
	})
	if err != nil {
		return fmt.Errorf("failed to get UDP relay: %w", err)
	}

	target := newSOCKS5Address(dstIP, dstPort)

	if f.logger.Core().Enabled(zap.DebugLevel) {
		f.logger.Debug("sending UDP via SOCKS5",
			zap.String("src", key.String()),
			zap.String("dst", target.String()),
			zap.Int("payload_len", len(payload)),
		)
	}

	if err := relay.Send(payload, target); err != nil {
		return fmt.Errorf("failed to send UDP: %w", err)
	}

	return nil
}

// startRelayReader spawns the demux reader goroutine for a relay association.
// Every datagram that arrives on the relay is matched to its responder by the
// SOCKS5 datagram header and delivered to the association's local client with
// the responder's address. When the reader stops (relay closed or socket
// error), the association is evicted from the pool so the next packet creates
// a fresh one.
func (f *RawUDPForwarder) startRelayReader(key netip.AddrPort, relay *socks5.UDPRelay, clientIP net.IP, clientPort uint16, reply stack.UDPReplyFunc) {
	go func() {
		defer f.relayPool.Remove(key, relay)

		bufp := udpBufPool.Get().(*[]byte)
		defer udpBufPool.Put(bufp)
		buf := *bufp

		for {
			n, responder, err := relay.Receive(buf)
			if err != nil {
				f.logger.Debug("UDP relay reader stopped",
					zap.String("key", key.String()),
					zap.Error(err),
				)
				return
			}
			if responder == nil || responder.IP == nil || reply == nil {
				continue
			}

			// Copy the payload: the pooled buffer is reused on the next read.
			payload := make([]byte, n)
			copy(payload, buf[:n])

			f.logger.Debug("received UDP response via SOCKS5",
				zap.String("key", key.String()),
				zap.String("responder", responder.String()),
				zap.Int("len", n),
			)

			reply(responder.IP, clientIP, responder.Port, clientPort, payload)
		}
	}()
}

// Close closes the relay pool.
func (f *RawUDPForwarder) Close() {
	f.relayPool.Close()
}

// relayTTL returns the relay TTL for testing.
func (f *RawUDPForwarder) relayTTL() time.Duration {
	return defaultRelayTTL
}
