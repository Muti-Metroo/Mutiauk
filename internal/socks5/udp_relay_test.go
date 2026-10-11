package socks5

import (
	"net"
	"testing"
	"time"
)

func newTestUDPRelay(t *testing.T) (*UDPRelay, net.Conn) {
	t.Helper()

	clientConn, serverConn := net.Pipe()
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("failed to create UDP socket: %v", err)
	}

	relay := &UDPRelay{
		controlConn: clientConn,
		relayConn:   udpConn,
		relayAddr:   &Address{Type: AddrTypeIPv4, IP: net.ParseIP("127.0.0.1").To4(), Port: 1080},
	}
	t.Cleanup(func() {
		relay.Close()
		serverConn.Close()
	})
	return relay, serverConn
}

func TestUDPRelay_MonitorControlConn_ServerClose(t *testing.T) {
	relay, serverConn := newTestUDPRelay(t)

	fired := make(chan struct{})
	relay.MonitorControlConn(func() { close(fired) })

	// The server tearing down the control connection must trigger the callback.
	serverConn.Close()

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not fire after control connection closed")
	}
}

func TestUDPRelay_MonitorControlConn_LocalClose(t *testing.T) {
	relay, _ := newTestUDPRelay(t)

	fired := make(chan struct{})
	relay.MonitorControlConn(func() { close(fired) })

	// A local Close() also terminates the control connection read.
	if err := relay.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not fire after local Close")
	}
}

func TestUDPRelay_MonitorControlConn_OnlyFirstMonitor(t *testing.T) {
	relay, serverConn := newTestUDPRelay(t)

	fired := make(chan struct{}, 2)
	relay.MonitorControlConn(func() { fired <- struct{}{} })
	relay.MonitorControlConn(func() { fired <- struct{}{} })

	serverConn.Close()

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not fire")
	}

	// Only one monitor may be active, so exactly one callback fires.
	select {
	case <-fired:
		t.Fatal("second MonitorControlConn call should have been ignored")
	case <-time.After(100 * time.Millisecond):
	}
}
