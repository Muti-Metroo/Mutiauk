package expose

import (
	"context"
	"io"
	"net"
	"sync"
	"time"

	"go.uber.org/zap"
)

// tcpHandler dials Target on each new stream and pipes bytes
// bidirectionally. Closing either side propagates to the other.
type tcpHandler struct {
	target string
	dialer *net.Dialer
	logger *zap.Logger
}

func newTCPHandler(target string, logger *zap.Logger) *tcpHandler {
	return &tcpHandler{
		target: target,
		dialer: &net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		},
		logger: logger,
	}
}

// Serve dials the target and copies bytes between conn and the dialed
// connection. Returns when either side EOFs or the context is
// cancelled.
func (h *tcpHandler) Serve(ctx context.Context, conn net.Conn) error {
	dialCtx, cancel := context.WithTimeout(ctx, h.dialer.Timeout)
	defer cancel()

	target, err := h.dialer.DialContext(dialCtx, "tcp", h.target)
	if err != nil {
		return err
	}
	defer target.Close()

	// Cancel both copies when the context dies.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = target.Close()
			_ = conn.Close()
		case <-stop:
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(target, conn)
		// Half-close the target's write side so the remote sees EOF
		// while we still drain its response.
		if cw, ok := target.(closeWriter); ok {
			_ = cw.CloseWrite()
		} else {
			_ = target.Close()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(conn, target)
		_ = conn.Close()
	}()
	wg.Wait()
	return nil
}

// closeWriter is the half-close interface implemented by *net.TCPConn.
type closeWriter interface {
	CloseWrite() error
}
