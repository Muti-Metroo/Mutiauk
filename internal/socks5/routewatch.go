package socks5

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// Route-watch frame types (server -> client), matching the Muti Metroo agent. Each
// frame is [type:1][length:u32 BE][payload:length].
const (
	routeWatchHeartbeat = 0x00 // length 0, no payload
	routeWatchSnapshot  = 0x01 // payload is DashboardResponse JSON
)

// maxRouteWatchFrame caps a single frame payload, matching the agent's bound.
const maxRouteWatchFrame = 8 << 20 // 8 MiB

// RouteWatchHeartbeatInterval is the agent's heartbeat cadence. The watcher
// treats a silence longer than a small multiple of this as a dead connection.
const RouteWatchHeartbeatInterval = 20 * time.Second

// RouteWatchStream is a long-lived stream of dashboard snapshots pushed by the
// agent over a single SOCKS5 connection (CmdRouteWatch).
type RouteWatchStream struct {
	conn net.Conn
}

// Next reads the next frame. For a SNAPSHOT frame it returns the JSON payload;
// for a HEARTBEAT or any unknown frame type it returns (nil, nil) so the caller
// can treat every successful read as a liveness signal. A read error (including
// a deadline) is returned as-is.
func (s *RouteWatchStream) Next() ([]byte, error) {
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(s.conn, hdr); err != nil {
		return nil, err
	}
	frameType := hdr[0]
	length := binary.BigEndian.Uint32(hdr[1:5])
	if length > maxRouteWatchFrame {
		return nil, fmt.Errorf("route watch frame too large: %d bytes", length)
	}

	var payload []byte
	if length > 0 {
		payload = make([]byte, length)
		if _, err := io.ReadFull(s.conn, payload); err != nil {
			return nil, err
		}
	}

	if frameType == routeWatchSnapshot {
		return payload, nil
	}
	// Heartbeat or unknown frame type: liveness only, no snapshot delivered.
	return nil, nil
}

// SetReadDeadline sets the read deadline on the underlying connection, letting
// the caller detect a stalled stream (missing heartbeats).
func (s *RouteWatchStream) SetReadDeadline(t time.Time) error {
	return s.conn.SetReadDeadline(t)
}

// Close terminates the stream and its underlying connection.
func (s *RouteWatchStream) Close() error {
	return s.conn.Close()
}

// RouteWatch opens a CmdRouteWatch (0x05) stream: the agent pushes the full
// dashboard snapshot on every relevant change, plus periodic heartbeats. This
// is a Muti Metroo extension. A *socks5.Error with Code ReplyCommandNotSupported
// means the agent does not support route-watch (see IsCommandNotSupported).
func (c *Client) RouteWatch(ctx context.Context) (*RouteWatchStream, error) {
	conn, err := c.dialServer(ctx)
	if err != nil {
		return nil, err
	}

	if c.Timeout > 0 {
		conn.SetDeadline(time.Now().Add(c.Timeout))
	}

	if err := c.handshake(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("handshake failed: %w", err)
	}

	if _, err := c.sendRequest(conn, CmdRouteWatch, routeWatchDummyAddr()); err != nil {
		conn.Close()
		return nil, fmt.Errorf("route watch request failed: %w", err)
	}

	// Clear the handshake deadline; the caller manages read deadlines for
	// liveness from here on.
	conn.SetDeadline(time.Time{})

	return &RouteWatchStream{conn: conn}, nil
}

// Snapshot performs a one-shot CmdSnapshot (0x06) request, returning a single
// dashboard snapshot JSON. The connection is closed before returning. Used by
// the route-trace path. A *socks5.Error with Code ReplyCommandNotSupported
// means the agent does not support snapshots.
func (c *Client) Snapshot(ctx context.Context) ([]byte, error) {
	conn, err := c.dialServer(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if c.Timeout > 0 {
		conn.SetDeadline(time.Now().Add(c.Timeout))
	}

	if err := c.handshake(conn); err != nil {
		return nil, fmt.Errorf("handshake failed: %w", err)
	}

	if _, err := c.sendRequest(conn, CmdSnapshot, routeWatchDummyAddr()); err != nil {
		return nil, fmt.Errorf("snapshot request failed: %w", err)
	}

	stream := &RouteWatchStream{conn: conn}
	for {
		payload, err := stream.Next()
		if err != nil {
			return nil, err
		}
		if payload != nil {
			return payload, nil
		}
		// Skip any heartbeat that precedes the snapshot.
	}
}

// ExposeStream opens a CmdExpose (0x07) stream: after the standard SOCKS5
// reply the connection becomes a full-duplex transport for an expose session
// (the agent opens streams, the client serves them). This is a Muti Metroo
// extension. A *socks5.Error with Code ReplyCommandNotSupported means the
// agent does not support expose over SOCKS (see IsCommandNotSupported); the
// caller should treat that as "agent too old" since there is no fallback.
func (c *Client) ExposeStream(ctx context.Context) (net.Conn, error) {
	conn, err := c.dialServer(ctx)
	if err != nil {
		return nil, err
	}

	if c.Timeout > 0 {
		conn.SetDeadline(time.Now().Add(c.Timeout))
	}

	if err := c.handshake(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("handshake failed: %w", err)
	}

	if _, err := c.sendRequest(conn, CmdExpose, routeWatchDummyAddr()); err != nil {
		conn.Close()
		return nil, fmt.Errorf("expose request failed: %w", err)
	}

	// Clear the handshake deadline; the expose session manages its own read
	// and write lifetimes from here on.
	conn.SetDeadline(time.Time{})

	return conn, nil
}

// routeWatchDummyAddr is the unused placeholder address sent with route-watch
// requests (a future revision may carry options/filters here).
func routeWatchDummyAddr() *Address {
	return &Address{Type: AddrTypeIPv4, IP: net.IPv4zero.To4(), Port: 0}
}

// IsCommandNotSupported reports whether err is a SOCKS5 reply indicating the
// command is not supported (REP=0x07), used to detect agents without
// route-watch so the client can fall back to dashboard polling.
func IsCommandNotSupported(err error) bool {
	var se *Error
	if errors.As(err, &se) {
		return se.Code == ReplyCommandNotSupported
	}
	return false
}
