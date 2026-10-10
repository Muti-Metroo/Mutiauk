package proxy

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/postalsys/mutiauk/internal/socks5"
	"go.uber.org/zap"
)

// udpBufPool reuses 64KB buffers for UDP packet handling to reduce GC pressure.
var udpBufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, DefaultUDPBufferSize)
		return &buf
	},
}

// RawUDPForwarder handles raw UDP packet forwarding through SOCKS5.
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

// HandleRawUDP forwards a UDP packet through SOCKS5 and returns the response.
func (f *RawUDPForwarder) HandleRawUDP(ctx context.Context, srcIP, dstIP net.IP, srcPort, dstPort uint16, payload []byte) ([]byte, error) {
	client := f.clientHolder.Get()

	relay, err := f.relayPool.GetOrCreate(ctx, addrKey(srcIP, srcPort), func(ctx context.Context) (*socks5.UDPRelay, error) {
		r, err := client.UDPAssociate(ctx, nil)
		if err != nil {
			return nil, err
		}
		f.logger.Debug("created new UDP relay",
			zap.String("key", addrKey(srcIP, srcPort)),
			zap.String("local", r.LocalAddr().String()),
			zap.String("relay_addr", r.RemoteAddr()),
		)
		return r, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get UDP relay: %w", err)
	}

	target := newSOCKS5Address(dstIP, dstPort)

	f.logger.Debug("sending UDP via SOCKS5",
		zap.String("src", addrKey(srcIP, srcPort)),
		zap.String("dst", target.String()),
		zap.Int("payload_len", len(payload)),
	)

	if err := relay.Send(payload, target); err != nil {
		return nil, fmt.Errorf("failed to send UDP: %w", err)
	}

	f.logger.Debug("waiting for UDP response")
	relay.SetReadDeadline(time.Now().Add(5 * time.Second))
	bufp := udpBufPool.Get().(*[]byte)
	buf := *bufp
	n, remoteAddr, err := relay.Receive(buf)
	if err != nil {
		udpBufPool.Put(bufp)
		f.logger.Debug("UDP receive timeout or error", zap.Error(err))
		return nil, nil
	}

	// Copy the response before returning the buffer to the pool
	response := make([]byte, n)
	copy(response, buf[:n])
	udpBufPool.Put(bufp)

	f.logger.Debug("received UDP response via SOCKS5",
		zap.String("remote", remoteAddr.String()),
		zap.Int("len", n),
	)

	return response, nil
}

// Close closes the relay pool.
func (f *RawUDPForwarder) Close() {
	f.relayPool.Close()
}

// relayTTL returns the relay TTL for testing.
func (f *RawUDPForwarder) relayTTL() time.Duration {
	return defaultRelayTTL
}
