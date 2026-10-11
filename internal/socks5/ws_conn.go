package socks5

import (
	"context"
	"encoding/base64"
	"errors"
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

	// mu guards opCtx/opCancel, reader, and retired. Every Read/Write runs
	// on opCtx; SetDeadline cancels and replaces it so a deadline change
	// also interrupts an operation that is already blocked, matching
	// net.Conn semantics. reader is the partially-consumed message reader;
	// it is bound to the opCtx that acquired it, so that context is retired
	// (canceled lazily) instead of canceled outright while reader is set.
	mu       sync.RWMutex
	opCtx    context.Context
	opCancel context.CancelFunc
	reader   io.Reader
	retired  []context.CancelFunc

	// readMu serializes Read calls.
	readMu sync.Mutex
}

// dialWebSocket connects to a SOCKS5 server over WebSocket.
// The wsURL should be ws:// or wss:// URL including the path (e.g., wss://server:8443/socks5).
// If username and password are provided, HTTP Basic Auth header is included in the request.
// keepAlive > 0 enables periodic WebSocket pings to keep NAT/firewall state
// warm and to detect dead peers; keepAlive <= 0 disables pings.
func dialWebSocket(ctx context.Context, wsURL string, timeout time.Duration, keepAlive time.Duration, username, password string) (net.Conn, error) {
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
	opCtx, opCancel := context.WithCancel(baseCtx)

	c := &wsConn{
		conn:       conn,
		baseCtx:    baseCtx,
		baseCancel: baseCancel,
		opCtx:      opCtx,
		opCancel:   opCancel,
	}
	if keepAlive > 0 {
		go c.pingLoop(keepAlive)
	}
	return c, nil
}

// pingLoop sends a WebSocket ping every interval until the connection is
// closed. nhooyr/websocket sends no pings on its own, so without this an
// idle WS connection behind a NAT or stateful firewall dies silently.
//
// nhooyr's Ping waits for the pong, but pongs are only processed while a
// Read is in flight, so on an idle connection the pong wait always times
// out. That timeout is therefore ignored: the ping frame itself already
// went out, which is what keeps NAT/firewall state warm, and an active
// reader (the common case on the data plane) still gets real pong
// confirmation. Only a failure to write the ping frame is treated as a
// dead connection, in which case the connection is closed so blocked
// readers wake up instead of hanging until the next write.
func (c *wsConn) pingLoop(interval time.Duration) {
	pingTimeout := interval / 2
	if pingTimeout < time.Second {
		pingTimeout = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.baseCtx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(c.baseCtx, pingTimeout)
			err := c.conn.Ping(ctx)
			cancel()
			if err == nil || errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			c.Close()
			return
		}
	}
}

// getContext returns the context for the current operation.
func (c *wsConn) getContext() context.Context {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.opCtx
}

// Read reads data from the WebSocket connection.
// WebSocket messages are read as binary frames and buffered for partial reads.
// Empty binary messages are skipped rather than surfaced as (0, nil), which
// would violate the io.Reader contract and spin callers such as io.Copy.
func (c *wsConn) Read(b []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	for {
		c.mu.RLock()
		reader := c.reader
		ctx := c.opCtx
		c.mu.RUnlock()

		// If we have a partial message buffered, read from it first
		if reader != nil {
			n, err := reader.Read(b)
			if err == io.EOF {
				c.mu.Lock()
				c.reader = nil
				c.cancelRetiredLocked()
				c.mu.Unlock()
				if n > 0 {
					return n, nil
				}
				// Empty remainder; move on to the next message.
				continue
			}
			return n, err
		}

		// Read next WebSocket message
		msgType, msgReader, err := c.conn.Reader(ctx)
		if err != nil {
			return 0, c.translateError(err)
		}

		if msgType != websocket.MessageBinary {
			return 0, fmt.Errorf("unexpected message type: %v", msgType)
		}

		// Try to read directly into the provided buffer
		n, err := msgReader.Read(b)
		if err == io.EOF {
			if n == 0 {
				// Zero-length binary message; skip it.
				continue
			}
			return n, nil
		}
		if err != nil {
			return n, err
		}

		// If the message is larger than the buffer, save the reader for the
		// next call. The reader stays bound to ctx; SetDeadline retires ctx
		// lazily for as long as this reader is buffered.
		c.mu.Lock()
		c.reader = msgReader
		c.mu.Unlock()
		return n, nil
	}
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
	if c.opCancel != nil {
		c.opCancel()
	}
	c.cancelRetiredLocked()
	c.mu.Unlock()
	// Cancel baseCtx which also cancels the operation context
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
//
// Setting or clearing the deadline replaces the operation context. When no
// message is mid-buffer, the old context is canceled immediately, so an
// operation already blocked on it (e.g. a pending Read waiting for the next
// message) is interrupted instead of running to the old deadline. Note that
// nhooyr/websocket closes the whole connection when a blocked operation's
// context is canceled - the same trade-off its own NetConn adapter makes -
// so callers must serialize deadline changes with active I/O. When a
// partially-consumed message reader exists it stays bound to the old
// context, which is then retired (canceled lazily once the message drains
// or on Close) rather than killed mid-message.
func (c *wsConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	old := c.opCancel
	if t.IsZero() {
		c.opCtx, c.opCancel = context.WithCancel(c.baseCtx)
	} else {
		c.opCtx, c.opCancel = context.WithDeadline(c.baseCtx, t)
	}
	if old != nil {
		if c.reader != nil {
			c.retired = append(c.retired, old)
		} else {
			old()
		}
	}
	c.mu.Unlock()
	return nil
}

// cancelRetiredLocked cancels and clears contexts retired by SetDeadline.
// Caller must hold c.mu.
func (c *wsConn) cancelRetiredLocked() {
	for _, cancel := range c.retired {
		cancel()
	}
	c.retired = nil
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
