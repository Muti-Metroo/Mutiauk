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

// bidirectionalCopy performs bidirectional data copy between two connections.
// It returns when either direction completes or the context is canceled.
// Both connections are closed to unblock any in-flight io.Copy goroutines.
func bidirectionalCopy(ctx context.Context, local, remote net.Conn, bufferSize int) error {
	errCh := make(chan error, 2)

	go func() {
		_, err := io.CopyBuffer(remote, local, make([]byte, bufferSize))
		errCh <- err
	}()

	go func() {
		_, err := io.CopyBuffer(local, remote, make([]byte, bufferSize))
		errCh <- err
	}()

	select {
	case <-ctx.Done():
		local.Close()
		remote.Close()
		return ctx.Err()
	case err := <-errCh:
		local.Close()
		remote.Close()
		return err
	}
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
