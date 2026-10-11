package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/postalsys/mutiauk/internal/socks5"
)

const (
	// DefaultTCPBufferSize is the default buffer size for TCP relay.
	DefaultTCPBufferSize = 32 * 1024
	// DefaultUDPBufferSize is the max UDP packet size.
	DefaultUDPBufferSize = 65535
)

// connKey builds a map key from source and destination addresses.
func connKey(src, dst net.Addr) string {
	return fmt.Sprintf("%s->%s", src.String(), dst.String())
}

// addrKey builds a map key from IP and port.
func addrKey(ip net.IP, port uint16) string {
	return fmt.Sprintf("%s:%d", ip, port)
}

// newSOCKS5Address creates a SOCKS5 Address from IP and port.
func newSOCKS5Address(ip net.IP, port uint16) *socks5.Address {
	if ip4 := ip.To4(); ip4 != nil {
		return &socks5.Address{
			Type: socks5.AddrTypeIPv4,
			IP:   ip4,
			Port: port,
		}
	}
	return &socks5.Address{
		Type: socks5.AddrTypeIPv6,
		IP:   ip,
		Port: port,
	}
}

// closeWriter is implemented by connections that support half-close
// (gonet.TCPConn on the TUN side, *net.TCPConn on the plain-TCP SOCKS5
// side). The WebSocket transport (wsConn) has no half-close semantics in
// its data plane.
type closeWriter interface {
	CloseWrite() error
}

// closeWrite propagates an EOF to the write side of c. For connections
// without half-close support (the WebSocket transport) it falls back to a
// full close: the WS data plane cannot signal FIN without closing the
// connection, so for request/response flows over WS the in-flight response
// may be lost - the same behavior as before half-close support was added.
func closeWrite(c net.Conn) {
	if cw, ok := c.(closeWriter); ok {
		cw.CloseWrite()
		return
	}
	c.Close()
}

// bidirectionalCopy performs bidirectional data copy between two connections.
// Each direction propagates a clean EOF as a half-close (CloseWrite) to the
// other connection, so a local FIN reaches the remote server while an
// in-flight response is still delivered back. A copy error tears the other
// direction down to unblock its reader. It returns once both directions are
// done or the context is canceled; both connections are closed on return.
func bidirectionalCopy(ctx context.Context, local, remote net.Conn, bufferSize int) error {
	errCh := make(chan error, 2)

	go func() {
		_, err := io.CopyBuffer(remote, local, make([]byte, bufferSize))
		if err == nil {
			closeWrite(remote)
		} else {
			remote.Close()
		}
		errCh <- err
	}()

	go func() {
		_, err := io.CopyBuffer(local, remote, make([]byte, bufferSize))
		if err == nil {
			closeWrite(local)
		} else {
			local.Close()
		}
		errCh <- err
	}()

	var firstErr error
	for i := 0; i < 2; i++ {
		select {
		case <-ctx.Done():
			local.Close()
			remote.Close()
			return ctx.Err()
		case err := <-errCh:
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}

	local.Close()
	remote.Close()
	return firstErr
}

// clientHolder provides thread-safe access to a SOCKS5 client.
type clientHolder struct {
	mu     sync.RWMutex
	client *socks5.Client
}

// Get returns the current client.
func (h *clientHolder) Get() *socks5.Client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.client
}

// Set updates the client.
func (h *clientHolder) Set(client *socks5.Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.client = client
}
