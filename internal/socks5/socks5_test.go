package socks5

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// --- Protocol Tests ---

func TestParseAddress(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *Address
		wantErr bool
	}{
		{
			name:  "IPv4 address",
			input: "192.168.1.1:8080",
			want: &Address{
				Type: AddrTypeIPv4,
				IP:   net.ParseIP("192.168.1.1").To4(),
				Port: 8080,
			},
		},
		{
			name:  "IPv6 address",
			input: "[::1]:443",
			want: &Address{
				Type: AddrTypeIPv6,
				IP:   net.ParseIP("::1"),
				Port: 443,
			},
		},
		{
			name:  "Domain address",
			input: "example.com:80",
			want: &Address{
				Type:   AddrTypeDomain,
				Domain: "example.com",
				Port:   80,
			},
		},
		{
			name:    "Invalid format - no port",
			input:   "192.168.1.1",
			wantErr: true,
		},
		{
			name:    "Invalid format - empty",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAddress(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseAddress() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if got.Type != tt.want.Type {
				t.Errorf("Type = %v, want %v", got.Type, tt.want.Type)
			}
			if got.Port != tt.want.Port {
				t.Errorf("Port = %v, want %v", got.Port, tt.want.Port)
			}
			if tt.want.Type == AddrTypeDomain {
				if got.Domain != tt.want.Domain {
					t.Errorf("Domain = %v, want %v", got.Domain, tt.want.Domain)
				}
			} else {
				if !got.IP.Equal(tt.want.IP) {
					t.Errorf("IP = %v, want %v", got.IP, tt.want.IP)
				}
			}
		})
	}
}

func TestAddressMarshalBinary(t *testing.T) {
	tests := []struct {
		name    string
		addr    *Address
		wantLen int
	}{
		{
			name: "IPv4",
			addr: &Address{
				Type: AddrTypeIPv4,
				IP:   net.ParseIP("10.0.0.1").To4(),
				Port: 1080,
			},
			wantLen: 1 + 4 + 2, // type + IPv4 + port
		},
		{
			name: "IPv6",
			addr: &Address{
				Type: AddrTypeIPv6,
				IP:   net.ParseIP("2001:db8::1"),
				Port: 443,
			},
			wantLen: 1 + 16 + 2, // type + IPv6 + port
		},
		{
			name: "Domain",
			addr: &Address{
				Type:   AddrTypeDomain,
				Domain: "example.com",
				Port:   80,
			},
			wantLen: 1 + 1 + 11 + 2, // type + len + domain + port
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.addr.MarshalBinary()
			if err != nil {
				t.Fatalf("MarshalBinary() error = %v", err)
			}
			if len(data) != tt.wantLen {
				t.Errorf("len(data) = %v, want %v", len(data), tt.wantLen)
			}
			if data[0] != tt.addr.Type {
				t.Errorf("data[0] = %v, want %v", data[0], tt.addr.Type)
			}
		})
	}
}

func TestAddressMarshalUnmarshal(t *testing.T) {
	tests := []struct {
		name string
		addr *Address
	}{
		{
			name: "IPv4",
			addr: &Address{
				Type: AddrTypeIPv4,
				IP:   net.ParseIP("192.168.1.100").To4(),
				Port: 8080,
			},
		},
		{
			name: "IPv6",
			addr: &Address{
				Type: AddrTypeIPv6,
				IP:   net.ParseIP("fe80::1"),
				Port: 443,
			},
		},
		{
			name: "Domain",
			addr: &Address{
				Type:   AddrTypeDomain,
				Domain: "test.example.org",
				Port:   3000,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.addr.MarshalBinary()
			if err != nil {
				t.Fatalf("MarshalBinary() error = %v", err)
			}

			got, consumed, err := ReadAddress(data)
			if err != nil {
				t.Fatalf("ReadAddress() error = %v", err)
			}
			if consumed != len(data) {
				t.Errorf("consumed = %v, want %v", consumed, len(data))
			}
			if got.Type != tt.addr.Type {
				t.Errorf("Type = %v, want %v", got.Type, tt.addr.Type)
			}
			if got.Port != tt.addr.Port {
				t.Errorf("Port = %v, want %v", got.Port, tt.addr.Port)
			}
		})
	}
}

func TestReadAddressErrors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"IPv4 too short", []byte{AddrTypeIPv4, 1, 2, 3}},
		{"IPv6 too short", []byte{AddrTypeIPv6, 1, 2, 3, 4, 5, 6, 7, 8}},
		{"Domain no length", []byte{AddrTypeDomain}},
		{"Domain too short", []byte{AddrTypeDomain, 10, 'a', 'b', 'c'}},
		{"Unknown type", []byte{0xFF, 1, 2, 3, 4, 5, 6, 7}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ReadAddress(tt.data)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestAddressString(t *testing.T) {
	tests := []struct {
		addr *Address
		want string
	}{
		{
			addr: &Address{Type: AddrTypeIPv4, IP: net.ParseIP("10.0.0.1").To4(), Port: 80},
			want: "10.0.0.1:80",
		},
		{
			addr: &Address{Type: AddrTypeIPv6, IP: net.ParseIP("::1"), Port: 443},
			want: "[::1]:443",
		},
		{
			addr: &Address{Type: AddrTypeDomain, Domain: "example.com", Port: 8080},
			want: "example.com:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.addr.String()
			if got != tt.want {
				t.Errorf("String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReplyMessage(t *testing.T) {
	tests := []struct {
		code byte
		want string
	}{
		{ReplySucceeded, "succeeded"},
		{ReplyGeneralFailure, "general SOCKS server failure"},
		{ReplyNotAllowed, "connection not allowed by ruleset"},
		{ReplyNetworkUnreachable, "network unreachable"},
		{ReplyHostUnreachable, "host unreachable"},
		{ReplyConnectionRefused, "connection refused"},
		{ReplyTTLExpired, "TTL expired"},
		{ReplyCommandNotSupported, "command not supported"},
		{ReplyAddressNotSupported, "address type not supported"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := ReplyMessage(tt.code)
			if got != tt.want {
				t.Errorf("ReplyMessage(%d) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

func TestError(t *testing.T) {
	err := &Error{
		Code:    ReplyConnectionRefused,
		Message: "connection refused",
	}

	got := err.Error()
	if got != "socks5 error (0x05): connection refused" {
		t.Errorf("Error() = %v", got)
	}
}

// --- Auth Tests ---

func TestNoAuth(t *testing.T) {
	auth := &NoAuth{}

	if auth.Method() != AuthNone {
		t.Errorf("Method() = %v, want %v", auth.Method(), AuthNone)
	}

	// Authenticate should do nothing
	if err := auth.Authenticate(nil); err != nil {
		t.Errorf("Authenticate() error = %v", err)
	}
}

func TestUserPassAuth(t *testing.T) {
	auth := &UserPassAuth{
		Username: "testuser",
		Password: "testpass",
	}

	if auth.Method() != AuthPassword {
		t.Errorf("Method() = %v, want %v", auth.Method(), AuthPassword)
	}
}

func TestNewAuthenticator(t *testing.T) {
	t.Run("no credentials", func(t *testing.T) {
		auth := NewAuthenticator("", "")
		if _, ok := auth.(*NoAuth); !ok {
			t.Error("expected NoAuth")
		}
	})

	t.Run("with credentials", func(t *testing.T) {
		auth := NewAuthenticator("user", "pass")
		up, ok := auth.(*UserPassAuth)
		if !ok {
			t.Error("expected UserPassAuth")
		}
		if up.Username != "user" || up.Password != "pass" {
			t.Error("credentials not set correctly")
		}
	})
}

// --- Client Tests ---

func TestNewClient(t *testing.T) {
	t.Run("with defaults", func(t *testing.T) {
		client := NewClient("127.0.0.1:1080", nil, 0, 0)
		if client.ServerAddr != "127.0.0.1:1080" {
			t.Errorf("ServerAddr = %v", client.ServerAddr)
		}
		if client.Timeout != 30*time.Second {
			t.Errorf("Timeout = %v, want 30s", client.Timeout)
		}
		if client.KeepAlive != 60*time.Second {
			t.Errorf("KeepAlive = %v, want 60s", client.KeepAlive)
		}
		if _, ok := client.Auth.(*NoAuth); !ok {
			t.Error("expected NoAuth as default")
		}
	})

	t.Run("with custom values", func(t *testing.T) {
		auth := &UserPassAuth{Username: "u", Password: "p"}
		client := NewClient("proxy:1080", auth, 10*time.Second, 30*time.Second)
		if client.Timeout != 10*time.Second {
			t.Errorf("Timeout = %v", client.Timeout)
		}
		if client.KeepAlive != 30*time.Second {
			t.Errorf("KeepAlive = %v", client.KeepAlive)
		}
		if client.Auth != auth {
			t.Error("Auth not set correctly")
		}
	})
}

// mockSOCKS5Server creates a simple mock SOCKS5 server for testing
type mockSOCKS5Server struct {
	listener     net.Listener
	authMethod   byte
	authSuccess  bool
	connectReply byte
	targetData   []byte // data to send to client after connect
}

func newMockSOCKS5Server(t *testing.T) *mockSOCKS5Server {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	srv := &mockSOCKS5Server{
		listener:     listener,
		authMethod:   AuthNone,
		authSuccess:  true,
		connectReply: ReplySucceeded,
	}

	go srv.serve(t)
	return srv
}

func (s *mockSOCKS5Server) serve(t *testing.T) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(t, conn)
	}
}

func (s *mockSOCKS5Server) handleConn(t *testing.T, conn net.Conn) {
	defer conn.Close()

	// Read method selection
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return
	}
	if header[0] != Version {
		t.Logf("unexpected version: %d", header[0])
		return
	}
	methods := make([]byte, header[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}

	// Send method response
	conn.Write([]byte{Version, s.authMethod})

	// Handle auth if needed
	if s.authMethod == AuthPassword {
		// Read auth request
		authHeader := make([]byte, 2)
		if _, err := io.ReadFull(conn, authHeader); err != nil {
			return
		}
		ulen := int(authHeader[1])
		username := make([]byte, ulen)
		io.ReadFull(conn, username)

		plenBuf := make([]byte, 1)
		io.ReadFull(conn, plenBuf)
		plen := int(plenBuf[0])
		password := make([]byte, plen)
		io.ReadFull(conn, password)

		// Send auth response
		status := byte(0x00)
		if !s.authSuccess {
			status = 0x01
		}
		conn.Write([]byte{0x01, status})

		if !s.authSuccess {
			return
		}
	}

	// Read connect request
	reqHeader := make([]byte, 4)
	if _, err := io.ReadFull(conn, reqHeader); err != nil {
		return
	}

	// Read address based on type
	switch reqHeader[3] {
	case AddrTypeIPv4:
		buf := make([]byte, 4+2)
		io.ReadFull(conn, buf)
	case AddrTypeIPv6:
		buf := make([]byte, 16+2)
		io.ReadFull(conn, buf)
	case AddrTypeDomain:
		lenBuf := make([]byte, 1)
		io.ReadFull(conn, lenBuf)
		buf := make([]byte, int(lenBuf[0])+2)
		io.ReadFull(conn, buf)
	}

	// Send reply with bound address (0.0.0.0:0)
	reply := []byte{
		Version,
		s.connectReply,
		0x00,         // reserved
		AddrTypeIPv4, // address type
		0, 0, 0, 0,   // IP
		0, 0, // port
	}
	conn.Write(reply)

	if s.connectReply != ReplySucceeded {
		return
	}

	// If we have target data, send it
	if len(s.targetData) > 0 {
		conn.Write(s.targetData)
	}

	// Echo any data back
	buf := make([]byte, 1024)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		conn.Write(buf[:n])
	}
}

func (s *mockSOCKS5Server) Addr() string {
	return s.listener.Addr().String()
}

func (s *mockSOCKS5Server) Close() {
	s.listener.Close()
}

func TestClientConnect(t *testing.T) {
	srv := newMockSOCKS5Server(t)
	defer srv.Close()

	client := NewClient(srv.Addr(), nil, 5*time.Second, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := client.Connect(ctx, "example.com:80")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer conn.Close()

	// Test that connection works (echo)
	testData := []byte("hello world")
	_, err = conn.Write(testData)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	buf := make([]byte, len(testData))
	_, err = io.ReadFull(conn, buf)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if !bytes.Equal(buf, testData) {
		t.Errorf("got %q, want %q", buf, testData)
	}
}

func TestClientConnectWithAuth(t *testing.T) {
	srv := newMockSOCKS5Server(t)
	srv.authMethod = AuthPassword
	srv.authSuccess = true
	defer srv.Close()

	auth := &UserPassAuth{Username: "user", Password: "pass"}
	client := NewClient(srv.Addr(), auth, 5*time.Second, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := client.Connect(ctx, "192.168.1.1:443")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	conn.Close()
}

func TestClientConnectAuthFailure(t *testing.T) {
	srv := newMockSOCKS5Server(t)
	srv.authMethod = AuthPassword
	srv.authSuccess = false
	defer srv.Close()

	auth := &UserPassAuth{Username: "user", Password: "wrong"}
	client := NewClient(srv.Addr(), auth, 5*time.Second, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Connect(ctx, "example.com:80")
	if err == nil {
		t.Error("expected error for failed auth")
	}
}

func TestClientConnectRefused(t *testing.T) {
	srv := newMockSOCKS5Server(t)
	srv.connectReply = ReplyConnectionRefused
	defer srv.Close()

	client := NewClient(srv.Addr(), nil, 5*time.Second, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Connect(ctx, "blocked.com:80")
	if err == nil {
		t.Error("expected error for refused connection")
	}

	// Should be a SOCKS5 error
	if sockErr, ok := err.(*Error); !ok {
		// The error might be wrapped
		t.Logf("error type: %T, message: %v", err, err)
	} else {
		if sockErr.Code != ReplyConnectionRefused {
			t.Errorf("error code = %v, want %v", sockErr.Code, ReplyConnectionRefused)
		}
	}
}

func TestClientConnectInvalidAddress(t *testing.T) {
	client := NewClient("127.0.0.1:1080", nil, 5*time.Second, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Connect(ctx, "invalid-no-port")
	if err == nil {
		t.Error("expected error for invalid address")
	}
}

func TestClientConnectServerDown(t *testing.T) {
	// Use a port that's definitely not listening
	client := NewClient("127.0.0.1:59999", nil, 1*time.Second, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := client.Connect(ctx, "example.com:80")
	if err == nil {
		t.Error("expected error when server is down")
	}
}

func TestAddressToUDPAddr(t *testing.T) {
	addr := &Address{
		Type: AddrTypeIPv4,
		IP:   net.ParseIP("10.0.0.1").To4(),
		Port: 5353,
	}

	udpAddr := addr.ToUDPAddr()
	if !udpAddr.IP.Equal(addr.IP) {
		t.Errorf("IP = %v, want %v", udpAddr.IP, addr.IP)
	}
	if udpAddr.Port != int(addr.Port) {
		t.Errorf("Port = %v, want %v", udpAddr.Port, addr.Port)
	}
}

func TestAddressToTCPAddr(t *testing.T) {
	addr := &Address{
		Type: AddrTypeIPv4,
		IP:   net.ParseIP("10.0.0.1").To4(),
		Port: 8080,
	}

	tcpAddr := addr.ToTCPAddr()
	if !tcpAddr.IP.Equal(addr.IP) {
		t.Errorf("IP = %v, want %v", tcpAddr.IP, addr.IP)
	}
	if tcpAddr.Port != int(addr.Port) {
		t.Errorf("Port = %v, want %v", tcpAddr.Port, addr.Port)
	}
}

func TestNewAddressFromUDPAddr(t *testing.T) {
	tests := []struct {
		name     string
		udpAddr  *net.UDPAddr
		wantType byte
		wantIP   net.IP
		wantPort uint16
	}{
		{
			name:     "nil address",
			udpAddr:  nil,
			wantType: AddrTypeIPv4,
			wantIP:   net.IPv4zero,
			wantPort: 0,
		},
		{
			name:     "nil IP",
			udpAddr:  &net.UDPAddr{IP: nil, Port: 1234},
			wantType: AddrTypeIPv4,
			wantIP:   net.IPv4zero,
			wantPort: 0,
		},
		{
			name:     "IPv4 address",
			udpAddr:  &net.UDPAddr{IP: net.ParseIP("192.168.1.1"), Port: 5353},
			wantType: AddrTypeIPv4,
			wantIP:   net.ParseIP("192.168.1.1").To4(),
			wantPort: 5353,
		},
		{
			name:     "IPv6 address",
			udpAddr:  &net.UDPAddr{IP: net.ParseIP("::1"), Port: 8080},
			wantType: AddrTypeIPv6,
			wantIP:   net.ParseIP("::1"),
			wantPort: 8080,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := NewAddressFromUDPAddr(tt.udpAddr)
			if addr.Type != tt.wantType {
				t.Errorf("Type = %v, want %v", addr.Type, tt.wantType)
			}
			if !addr.IP.Equal(tt.wantIP) {
				t.Errorf("IP = %v, want %v", addr.IP, tt.wantIP)
			}
			if addr.Port != tt.wantPort {
				t.Errorf("Port = %v, want %v", addr.Port, tt.wantPort)
			}
		})
	}
}

// TestUserPassAuthAuthenticate tests the actual authentication protocol
func TestUserPassAuthAuthenticate(t *testing.T) {
	// Create a pipe to simulate connection
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	auth := &UserPassAuth{
		Username: "testuser",
		Password: "testpass",
	}

	// Server side: read and respond
	go func() {
		// Read version
		buf := make([]byte, 1)
		serverConn.Read(buf)
		if buf[0] != 0x01 {
			t.Errorf("expected auth version 0x01, got 0x%02x", buf[0])
		}

		// Read username length and username
		serverConn.Read(buf)
		ulen := int(buf[0])
		username := make([]byte, ulen)
		serverConn.Read(username)

		// Read password length and password
		serverConn.Read(buf)
		plen := int(buf[0])
		password := make([]byte, plen)
		serverConn.Read(password)

		// Verify credentials
		if string(username) != "testuser" || string(password) != "testpass" {
			serverConn.Write([]byte{0x01, 0x01}) // failure
		} else {
			serverConn.Write([]byte{0x01, 0x00}) // success
		}
	}()

	// Client side: authenticate
	err := auth.Authenticate(clientConn)
	if err != nil {
		t.Errorf("Authenticate() error = %v", err)
	}
}
