package proxy

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/postalsys/mutiauk/internal/nat"
	"github.com/postalsys/mutiauk/internal/socks5"
	"go.uber.org/zap"
)

const defaultRelayTTL = 5 * time.Minute

// UDPForwarder forwards UDP packets through SOCKS5 UDP ASSOCIATE.
type UDPForwarder struct {
	clientHolder
	natTable   *nat.Table
	logger     *zap.Logger
	relayPool  *RelayPool
	bufferSize int
}

// NewUDPForwarder creates a new UDP forwarder.
func NewUDPForwarder(client *socks5.Client, natTable *nat.Table, logger *zap.Logger) *UDPForwarder {
	return &UDPForwarder{
		clientHolder: clientHolder{client: client},
		natTable:     natTable,
		logger:       logger,
		relayPool:    NewRelayPool(defaultRelayTTL, logger),
		bufferSize:   DefaultUDPBufferSize,
	}
}

// UpdateClient updates the SOCKS5 client and clears the relay pool.
func (f *UDPForwarder) UpdateClient(client *socks5.Client) {
	f.clientHolder.Set(client)
	f.relayPool.Clear()
}

// HandleUDP handles a UDP packet from the TUN interface.
func (f *UDPForwarder) HandleUDP(ctx context.Context, conn net.PacketConn, srcAddr, dstAddr net.Addr) error {
	defer conn.Close()

	srcUDP := srcAddr.(*net.UDPAddr)
	dstUDP := dstAddr.(*net.UDPAddr)
	srcAddrPort := netip.MustParseAddrPort(srcUDP.String())
	dstAddrPort := netip.MustParseAddrPort(dstUDP.String())

	client := f.clientHolder.Get()
	relay, err := f.relayPool.GetOrCreate(ctx, srcUDP.String(), func(ctx context.Context) (*socks5.UDPRelay, error) {
		return client.UDPAssociate(ctx, nil)
	})
	if err != nil {
		f.logger.Error("failed to get UDP relay",
			zap.String("src", srcAddr.String()),
			zap.Error(err),
		)
		return fmt.Errorf("failed to get UDP relay: %w", err)
	}

	entry := &nat.Entry{
		Protocol: "udp",
		SrcAddr:  srcAddrPort,
		DstAddr:  dstAddrPort,
		UDPRelay: relay,
		State:    nat.ConnStateEstablished,
	}

	if err := f.natTable.Insert(entry); err != nil {
		f.logger.Debug("NAT entry already exists", zap.Error(err))
	}
	defer f.natTable.Remove("udp", srcAddrPort, dstAddrPort)

	f.logger.Debug("UDP forwarding started",
		zap.String("src", srcAddr.String()),
		zap.String("dst", dstAddr.String()),
		zap.String("relay_local", relay.LocalAddr().String()),
		zap.String("relay_remote", relay.RemoteAddr()),
	)

	return f.relay(ctx, conn, relay, srcUDP, dstUDP)
}

// relay performs bidirectional UDP forwarding.
func (f *UDPForwarder) relay(ctx context.Context, local net.PacketConn, relay *socks5.UDPRelay, srcUDP, dstUDP *net.UDPAddr) error {
	errCh := make(chan error, 2)
	target := newSOCKS5Address(dstUDP.IP, uint16(dstUDP.Port))

	// Local -> Relay (via SOCKS5)
	go func() {
		buf := make([]byte, f.bufferSize)
		for {
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			default:
			}

			local.SetReadDeadline(time.Now().Add(30 * time.Second))
			n, from, err := local.ReadFrom(buf)
			if err != nil {
				f.logger.Debug("UDP read from local failed", zap.Error(err))
				errCh <- err
				return
			}

			f.logger.Debug("UDP sending to relay",
				zap.Int("bytes", n),
				zap.String("from", from.String()),
				zap.String("target", target.String()),
				zap.String("data_hex", fmt.Sprintf("%x", buf[:min(n, 20)])),
			)

			if err := relay.Send(buf[:n], target); err != nil {
				f.logger.Debug("UDP send to relay failed", zap.Error(err))
				errCh <- err
				return
			}
			f.logger.Debug("UDP sent to relay successfully", zap.Int("bytes", n))
		}
	}()

	// Relay -> Local (via SOCKS5)
	go func() {
		buf := make([]byte, f.bufferSize)
		for {
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			default:
			}

			relay.SetReadDeadline(time.Now().Add(30 * time.Second))
			n, remoteAddr, err := relay.Receive(buf)
			if err != nil {
				f.logger.Debug("UDP receive from relay failed", zap.Error(err))
				errCh <- err
				return
			}
			f.logger.Debug("UDP received from relay",
				zap.Int("bytes", n),
				zap.String("remote", remoteAddr.String()),
			)

			// Resolve domain if needed
			udpAddr := remoteAddr.ToUDPAddr()
			if udpAddr.IP == nil && remoteAddr.Domain != "" {
				ips, err := net.LookupIP(remoteAddr.Domain)
				if err != nil || len(ips) == 0 {
					continue
				}
				udpAddr.IP = ips[0]
			}

			if _, err := local.WriteTo(buf[:n], srcUDP); err != nil {
				f.logger.Debug("failed to write UDP response",
					zap.String("src", udpAddr.String()),
					zap.Error(err),
				)
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// Close closes the relay pool.
func (f *UDPForwarder) Close() {
	f.relayPool.Close()
}

// relayTTL returns the relay TTL for testing.
func (f *UDPForwarder) relayTTL() time.Duration {
	return defaultRelayTTL
}
