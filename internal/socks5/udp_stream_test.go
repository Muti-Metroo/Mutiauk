package socks5

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// udpStreamServer is a minimal SOCKS5 server for UDP STREAM tests. With
// streamSupported it answers UDP STREAM and echoes every frame back;
// otherwise it rejects UDP STREAM with "command not supported" and answers
// UDP ASSOCIATE with a relay address on 127.0.0.1.
type udpStreamServer struct {
	ln              net.Listener
	streamSupported bool
	streamRequests  atomic.Int32
}

func newUDPStreamServer(t *testing.T, streamSupported bool) *udpStreamServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &udpStreamServer{ln: ln, streamSupported: streamSupported}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(conn)
		}
	}()
	return s
}

func (s *udpStreamServer) handle(conn net.Conn) {
	defer conn.Close()

	greeting := make([]byte, 2)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		return
	}
	if _, err := io.ReadFull(conn, make([]byte, greeting[1])); err != nil {
		return
	}
	conn.Write([]byte{Version, AuthNone})

	// VER CMD RSV ATYP(IPv4) ADDR(4) PORT(2)
	req := make([]byte, 10)
	if _, err := io.ReadFull(conn, req); err != nil {
		return
	}
	reply := func(code byte, port uint16) {
		buf := []byte{Version, code, 0, AddrTypeIPv4, 127, 0, 0, 1, 0, 0}
		binary.BigEndian.PutUint16(buf[8:], port)
		conn.Write(buf)
	}

	switch req[1] {
	case CmdUDPStream:
		s.streamRequests.Add(1)
		if !s.streamSupported {
			reply(ReplyCommandNotSupported, 0)
			return
		}
		reply(ReplySucceeded, 0)
		for {
			lenBuf := make([]byte, 2)
			if _, err := io.ReadFull(conn, lenBuf); err != nil {
				return
			}
			body := make([]byte, binary.BigEndian.Uint16(lenBuf))
			if _, err := io.ReadFull(conn, body); err != nil {
				return
			}
			conn.Write(append(lenBuf, body...))
		}
	case CmdUDPAssociate:
		udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			return
		}
		defer udpConn.Close()
		reply(ReplySucceeded, uint16(udpConn.LocalAddr().(*net.UDPAddr).Port))
		io.Copy(io.Discard, conn)
	default:
		reply(ReplyCommandNotSupported, 0)
	}
}

func TestUDPAssociate_StreamRoundTrip(t *testing.T) {
	srv := newUDPStreamServer(t, true)
	client := NewClient(srv.ln.Addr().String(), nil, 5*time.Second, 0)

	relay, err := client.UDPAssociate(context.Background(), nil)
	if err != nil {
		t.Fatalf("UDPAssociate: %v", err)
	}
	defer relay.Close()

	if !relay.IsStream() {
		t.Fatal("relay should use UDP STREAM when the server supports it")
	}

	target := &Address{Type: AddrTypeIPv4, IP: net.IPv4(10, 0, 0, 5).To4(), Port: 53}
	if err := relay.Send([]byte("hello"), target); err != nil {
		t.Fatalf("Send: %v", err)
	}

	relay.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	n, from, err := relay.Receive(buf)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if string(buf[:n]) != "hello" {
		t.Errorf("payload = %q, want %q", buf[:n], "hello")
	}
	if !from.IP.Equal(target.IP) || from.Port != 53 {
		t.Errorf("source = %s, want %s", from, target)
	}
}

func TestUDPAssociate_StreamReceiveEndsOnClose(t *testing.T) {
	srv := newUDPStreamServer(t, true)
	client := NewClient(srv.ln.Addr().String(), nil, 5*time.Second, 0)

	relay, err := client.UDPAssociate(context.Background(), nil)
	if err != nil {
		t.Fatalf("UDPAssociate: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, _, err := relay.Receive(make([]byte, 1500))
		done <- err
	}()
	relay.Close()

	select {
	case err := <-done:
		if err == nil {
			t.Error("Receive should fail once the relay is closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Receive did not return after Close")
	}
}

func TestUDPAssociate_FallsBackToAssociate(t *testing.T) {
	srv := newUDPStreamServer(t, false)
	client := NewClient(srv.ln.Addr().String(), nil, 5*time.Second, 0)

	for i := 0; i < 2; i++ {
		relay, err := client.UDPAssociate(context.Background(), nil)
		if err != nil {
			t.Fatalf("UDPAssociate #%d: %v", i+1, err)
		}
		if relay.IsStream() {
			t.Errorf("UDPAssociate #%d: relay should fall back to UDP ASSOCIATE", i+1)
		}
		if relay.RelayUDPAddr() == nil {
			t.Errorf("UDPAssociate #%d: relay address missing", i+1)
		}
		relay.Close()
	}

	// The refusal is remembered, so only the first association probes.
	if got := srv.streamRequests.Load(); got != 1 {
		t.Errorf("UDP STREAM requests = %d, want 1", got)
	}
}
