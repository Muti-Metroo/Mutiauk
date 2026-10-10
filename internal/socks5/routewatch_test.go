package socks5

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// mockRouteWatchServer is a minimal SOCKS5 server for route-watch tests. It
// performs the no-auth greeting, reads one request, replies with replyCode,
// and (on success) invokes stream to write frames to the connection.
type mockRouteWatchServer struct {
	ln        net.Listener
	replyCode byte
	wantCmd   byte
	gotCmd    chan byte
	stream    func(conn net.Conn)
}

func newMockRouteWatchServer(t *testing.T, replyCode byte, stream func(conn net.Conn)) *mockRouteWatchServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	m := &mockRouteWatchServer{
		ln:        ln,
		replyCode: replyCode,
		gotCmd:    make(chan byte, 1),
		stream:    stream,
	}
	t.Cleanup(func() { ln.Close() })
	go m.serve()
	return m
}

func (m *mockRouteWatchServer) addr() string { return m.ln.Addr().String() }

func (m *mockRouteWatchServer) serve() {
	conn, err := m.ln.Accept()
	if err != nil {
		return
	}

	// Greeting: VER, NMETHODS, METHODS...
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		conn.Close()
		return
	}
	methods := make([]byte, int(hdr[1]))
	io.ReadFull(conn, methods)
	conn.Write([]byte{Version, AuthNone}) // select no-auth

	// Request: VER, CMD, RSV, ATYP(=1), 4-byte IP, 2-byte port.
	req := make([]byte, 10)
	if _, err := io.ReadFull(conn, req); err != nil {
		conn.Close()
		return
	}
	select {
	case m.gotCmd <- req[1]:
	default:
	}

	// Reply: VER, REP, RSV, ATYP(=1), 0.0.0.0, port 0.
	conn.Write([]byte{Version, m.replyCode, 0x00, AddrTypeIPv4, 0, 0, 0, 0, 0, 0})

	if m.replyCode != ReplySucceeded {
		conn.Close()
		return
	}
	if m.stream != nil {
		m.stream(conn)
	}
}

// writeTestFrame writes one route-watch frame.
func writeTestFrame(conn net.Conn, frameType byte, payload []byte) {
	buf := make([]byte, 5+len(payload))
	buf[0] = frameType
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(payload)))
	copy(buf[5:], payload)
	conn.Write(buf)
}

func TestRouteWatch_ReceivesSnapshotsAndHeartbeats(t *testing.T) {
	srv := newMockRouteWatchServer(t, ReplySucceeded, func(conn net.Conn) {
		writeTestFrame(conn, routeWatchSnapshot, []byte(`{"v":1}`))
		writeTestFrame(conn, routeWatchHeartbeat, nil)
		writeTestFrame(conn, routeWatchSnapshot, []byte(`{"v":2}`))
		time.Sleep(50 * time.Millisecond)
		conn.Close()
	})

	client := NewClient(srv.addr(), nil, 2*time.Second, 0)
	stream, err := client.RouteWatch(context.Background())
	if err != nil {
		t.Fatalf("RouteWatch: %v", err)
	}
	defer stream.Close()

	if got := <-srv.gotCmd; got != CmdRouteWatch {
		t.Fatalf("server saw cmd 0x%02x, want CmdRouteWatch", got)
	}

	// First snapshot.
	p, err := stream.Next()
	if err != nil || string(p) != `{"v":1}` {
		t.Fatalf("first Next = %q, %v; want {\"v\":1}", p, err)
	}
	// Heartbeat -> (nil, nil).
	p, err = stream.Next()
	if err != nil || p != nil {
		t.Fatalf("heartbeat Next = %q, %v; want nil,nil", p, err)
	}
	// Second snapshot.
	p, err = stream.Next()
	if err != nil || string(p) != `{"v":2}` {
		t.Fatalf("second Next = %q, %v; want {\"v\":2}", p, err)
	}
}

func TestRouteWatch_UnsupportedDetected(t *testing.T) {
	srv := newMockRouteWatchServer(t, ReplyCommandNotSupported, nil)

	client := NewClient(srv.addr(), nil, 2*time.Second, 0)
	_, err := client.RouteWatch(context.Background())
	if err == nil {
		t.Fatal("expected error for unsupported command")
	}
	if !IsCommandNotSupported(err) {
		t.Fatalf("IsCommandNotSupported(%v) = false, want true", err)
	}
}

func TestSnapshot_OneShot(t *testing.T) {
	srv := newMockRouteWatchServer(t, ReplySucceeded, func(conn net.Conn) {
		writeTestFrame(conn, routeWatchSnapshot, []byte(`{"trace":true}`))
		conn.Close()
	})

	client := NewClient(srv.addr(), nil, 2*time.Second, 0)
	snap, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if string(snap) != `{"trace":true}` {
		t.Fatalf("Snapshot = %q, want {\"trace\":true}", snap)
	}
	if got := <-srv.gotCmd; got != CmdSnapshot {
		t.Fatalf("server saw cmd 0x%02x, want CmdSnapshot", got)
	}
}

func TestRouteWatch_RejectsOversizeFrame(t *testing.T) {
	srv := newMockRouteWatchServer(t, ReplySucceeded, func(conn net.Conn) {
		// Advertise a length beyond the cap without sending the body.
		var hdr [5]byte
		hdr[0] = routeWatchSnapshot
		binary.BigEndian.PutUint32(hdr[1:5], maxRouteWatchFrame+1)
		conn.Write(hdr[:])
		time.Sleep(50 * time.Millisecond)
		conn.Close()
	})

	client := NewClient(srv.addr(), nil, 2*time.Second, 0)
	stream, err := client.RouteWatch(context.Background())
	if err != nil {
		t.Fatalf("RouteWatch: %v", err)
	}
	defer stream.Close()

	if _, err := stream.Next(); err == nil {
		t.Fatal("expected error for oversize frame")
	}
}
