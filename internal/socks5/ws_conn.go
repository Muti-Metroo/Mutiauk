package socks5

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

// wsConn wraps websocket.Conn to implement net.Conn.
// This allows the SOCKS5 protocol to work over WebSocket transport.
type wsConn struct {
	conn       *websocket.Conn
	baseCtx    context.Context
	baseCancel context.CancelFunc

	mu             sync.RWMutex
	deadlineCtx    context.Context
	deadlineCancel context.CancelFunc

	readMu sync.Mutex
	reader io.Reader
}

// dialWebSocket connects to a SOCKS5 server over WebSocket.
// The wsURL should be ws:// or wss:// URL including the path (e.g., wss://server:8443/socks5).
// If username and password are provided, HTTP Basic Auth header is included in the request.
func dialWebSocket(ctx context.Context, wsURL string, timeout time.Duration, username, password string) (net.Conn, error) {
	dialCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	dialOpts := &websocket.DialOptions{
		Subprotocols: []string{"socks5"},
	}

	// Add HTTP Basic Auth header if credentials are provided
	if username != "" || password != "" {
		credentials := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		dialOpts.HTTPHeader = http.Header{
			"Authorization": []string{"Basic " + credentials},
		}
	}

	conn, _, err := websocket.Dial(dialCtx, wsURL, dialOpts)
	if err != nil {
		return nil, fmt.Errorf("websocket dial failed: %w", err)
	}

	// Create a long-lived context for the connection
	baseCtx, baseCancel := context.WithCancel(context.Background())

	return &wsConn{
		conn:       conn,
		baseCtx:    baseCtx,
		baseCancel: baseCancel,
	}, nil
}

// getContext returns a context for the current operation, respecting any deadline set.
func (c *wsConn) getContext() context.Context {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.deadlineCtx != nil {
		return c.deadlineCtx
	}
	return c.baseCtx
}

// Read reads data from the WebSocket connection.
// WebSocket messages are read as binary frames and buffered for partial reads.
func (c *wsConn) Read(b []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	// If we have a partial message buffered, read from it first
	if c.reader != nil {
		n, err := c.reader.Read(b)
		if err == io.EOF {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			// Fall through to read next message
		} else {
			return n, err
		}
	}

	ctx := c.getContext()

	// Read next WebSocket message
	msgType, reader, err := c.conn.Reader(ctx)
	if err != nil {
		return 0, c.translateError(err)
	}

	if msgType != websocket.MessageBinary {
		return 0, fmt.Errorf("unexpected message type: %v", msgType)
	}

	// Try to read directly into the provided buffer
	n, err := reader.Read(b)
	if err == io.EOF {
		return n, nil
	}
	if err != nil {
		return n, err
	}

	// If the message is larger than the buffer, save the reader for next call
	c.reader = reader
	return n, nil
}

// Write writes data as a binary WebSocket message.
func (c *wsConn) Write(b []byte) (int, error) {
	ctx := c.getContext()
	err := c.conn.Write(ctx, websocket.MessageBinary, b)
	if err != nil {
		return 0, c.translateError(err)
	}
	return len(b), nil
}

// Close closes the WebSocket connection with a normal closure status.
func (c *wsConn) Close() error {
	c.mu.Lock()
	if c.deadlineCancel != nil {
		c.deadlineCancel()
		c.deadlineCancel = nil
		c.deadlineCtx = nil
	}
	c.mu.Unlock()
	// Cancel baseCtx which also cancels all derived deadline contexts
	c.baseCancel()
	return c.conn.Close(websocket.StatusNormalClosure, "")
}

// LocalAddr returns nil as WebSocket doesn't expose local address directly.
func (c *wsConn) LocalAddr() net.Addr {
	return nil
}

// RemoteAddr returns nil as WebSocket doesn't expose remote address directly.
func (c *wsConn) RemoteAddr() net.Addr {
	return nil
}

// SetDeadline sets both read and write deadlines.
// WebSocket uses a single deadline for all operations.
// Previous deadline contexts are not cancelled here because the websocket
// library may still reference them internally. They expire naturally when
// their deadline passes, and all are cancelled when baseCtx is cancelled
// on Close().
func (c *wsConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	if t.IsZero() {
		c.deadlineCtx = nil
		c.deadlineCancel = nil
	} else {
		c.deadlineCtx, c.deadlineCancel = context.WithDeadline(c.baseCtx, t)
	}
	c.mu.Unlock()
	return nil
}

// SetReadDeadline delegates to SetDeadline (WebSocket uses unified deadline).
func (c *wsConn) SetReadDeadline(t time.Time) error { return c.SetDeadline(t) }

// SetWriteDeadline delegates to SetDeadline (WebSocket uses unified deadline).
func (c *wsConn) SetWriteDeadline(t time.Time) error { return c.SetDeadline(t) }

// translateError converts WebSocket-specific errors to standard net errors.
func (c *wsConn) translateError(err error) error {
	if websocket.CloseStatus(err) != -1 {
		return io.EOF
	}
	return err
}
