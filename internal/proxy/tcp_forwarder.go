package proxy

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"

	"github.com/postalsys/mutiauk/internal/nat"
	"github.com/postalsys/mutiauk/internal/socks5"
	"go.uber.org/zap"
)

// TCPForwarder forwards TCP connections through SOCKS5.
type TCPForwarder struct {
	clientHolder
	natTable   *nat.Table
	logger     *zap.Logger
	bufferSize int

	// pendingConns holds pre-established SOCKS5 connections waiting for local endpoint.
	pendingConns sync.Map
}

// NewTCPForwarder creates a new TCP forwarder.
func NewTCPForwarder(client *socks5.Client, natTable *nat.Table, logger *zap.Logger) *TCPForwarder {
	return &TCPForwarder{
		clientHolder: clientHolder{client: client},
		natTable:     natTable,
		logger:       logger,
		bufferSize:   DefaultTCPBufferSize,
	}
}

// UpdateClient updates the SOCKS5 client.
func (f *TCPForwarder) UpdateClient(client *socks5.Client) {
	f.clientHolder.Set(client)
}

// PreConnect establishes the SOCKS5 connection BEFORE the TCP handshake is completed.
// This allows us to reject connections to unreachable destinations with RST,
// making port scanning through the proxy accurate.
func (f *TCPForwarder) PreConnect(ctx context.Context, srcAddr, dstAddr net.Addr) (net.Conn, error) {
	client := f.clientHolder.Get()
	targetAddr := dstAddr.(*net.TCPAddr).String()

	proxyConn, err := client.Connect(ctx, targetAddr)
	if err != nil {
		f.logger.Debug("SOCKS5 pre-connect failed",
			zap.String("src", srcAddr.String()),
			zap.String("dst", dstAddr.String()),
			zap.Error(err),
		)
		return nil, err
	}

	f.pendingConns.Store(connKey(srcAddr, dstAddr), proxyConn)

	f.logger.Debug("SOCKS5 pre-connect succeeded",
		zap.String("src", srcAddr.String()),
		zap.String("dst", dstAddr.String()),
	)

	return proxyConn, nil
}

// CleanupPending removes and closes a pending pre-established connection.
// Called when CreateEndpoint fails after a successful PreConnect.
func (f *TCPForwarder) CleanupPending(srcAddr, dstAddr net.Addr) {
	key := connKey(srcAddr, dstAddr)
	if pending, ok := f.pendingConns.LoadAndDelete(key); ok {
		pending.(net.Conn).Close()
	}
}

// HandleTCP handles a TCP connection from the TUN interface.
func (f *TCPForwarder) HandleTCP(ctx context.Context, conn net.Conn, srcAddr, dstAddr net.Addr) error {
	defer conn.Close()

	srcTCP := srcAddr.(*net.TCPAddr)
	dstTCP := dstAddr.(*net.TCPAddr)
	srcAddrPort := netip.MustParseAddrPort(srcTCP.String())
	dstAddrPort := netip.MustParseAddrPort(dstTCP.String())

	if entry, exists := f.natTable.Lookup("tcp", srcAddrPort, dstAddrPort); exists && entry.ProxyConn != nil {
		f.logger.Debug("using existing NAT entry",
			zap.String("src", srcAddr.String()),
			zap.String("dst", dstAddr.String()),
		)
	}

	key := connKey(srcAddr, dstAddr)
	var proxyConn net.Conn

	if pending, ok := f.pendingConns.LoadAndDelete(key); ok {
		proxyConn = pending.(net.Conn)
		f.logger.Debug("using pre-established SOCKS5 connection",
			zap.String("src", srcAddr.String()),
			zap.String("dst", dstAddr.String()),
		)
	} else {
		client := f.clientHolder.Get()
		var err error
		proxyConn, err = client.Connect(ctx, dstTCP.String())
		if err != nil {
			f.logger.Error("SOCKS5 connect failed",
				zap.String("src", srcAddr.String()),
				zap.String("dst", dstAddr.String()),
				zap.Error(err),
			)
			return fmt.Errorf("SOCKS5 connect failed: %w", err)
		}
	}
	defer proxyConn.Close()

	entry := &nat.Entry{
		Protocol:  "tcp",
		SrcAddr:   srcAddrPort,
		DstAddr:   dstAddrPort,
		ProxyConn: proxyConn,
		State:     nat.ConnStateEstablished,
	}

	if err := f.natTable.Insert(entry); err != nil {
		f.logger.Debug("failed to insert NAT entry (may already exist)", zap.Error(err))
	}
	defer f.natTable.Remove("tcp", srcAddrPort, dstAddrPort)

	f.logger.Debug("TCP forwarding established",
		zap.String("src", srcAddr.String()),
		zap.String("dst", dstAddr.String()),
	)

	return bidirectionalCopy(ctx, conn, proxyConn, f.bufferSize)
}
