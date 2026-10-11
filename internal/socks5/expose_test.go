package socks5

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// TestExposeStream_SuccessReturnsRawConn verifies CmdExpose handshake: the
// client sends CmdExpose, and on REP=0x00 the returned conn is the live,
// full-duplex stream (the server can write bytes the client reads).
func TestExposeStream_SuccessReturnsRawConn(t *testing.T) {
	srv := newMockRouteWatchServer(t, ReplySucceeded, func(conn net.Conn) {
		// After the reply, push a sentinel byte sequence the client reads.
		_, _ = conn.Write([]byte("expose-up"))
		time.Sleep(50 * time.Millisecond)
		conn.Close()
	})

	client := NewClient(srv.addr(), nil, 2*time.Second, 0)
	conn, err := client.ExposeStream(context.Background())
	if err != nil {
		t.Fatalf("ExposeStream: %v", err)
	}
	defer conn.Close()

	if got := <-srv.gotCmd; got != CmdExpose {
		t.Fatalf("server saw cmd 0x%02x, want CmdExpose (0x%02x)", got, CmdExpose)
	}

	buf := make([]byte, len("expose-up"))
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if string(buf) != "expose-up" {
		t.Errorf("sentinel: got %q want %q", buf, "expose-up")
	}
}

func TestExposeStream_UnsupportedDetected(t *testing.T) {
	srv := newMockRouteWatchServer(t, ReplyCommandNotSupported, nil)

	client := NewClient(srv.addr(), nil, 2*time.Second, 0)
	_, err := client.ExposeStream(context.Background())
	if err == nil {
		t.Fatal("expected error for unsupported command")
	}
	if !IsCommandNotSupported(err) {
		t.Fatalf("IsCommandNotSupported(%v) = false, want true", err)
	}
}
