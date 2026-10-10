package socks5

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func TestIsWebSocketURL(t *testing.T) {
	tests := []struct {
		name       string
		serverAddr string
		want       bool
	}{
		{"ws scheme", "ws://localhost:8080/socks5", true},
		{"wss scheme", "wss://relay.example.com/socks5", true},
		{"http scheme", "http://localhost:8080", false},
		{"https scheme", "https://localhost:8080", false},
		{"host:port", "127.0.0.1:1080", false},
		{"hostname:port", "proxy.example.com:1080", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{ServerAddr: tt.serverAddr}
			got := client.isWebSocketURL()
			if got != tt.want {
				t.Errorf("isWebSocketURL() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildWebSocketURL(t *testing.T) {
	tests := []struct {
		name       string
		serverAddr string
		wsPath     string
		want       string
	}{
		{
			name:       "already ws URL",
			serverAddr: "ws://localhost:8080/custom",
			wsPath:     "/socks5",
			want:       "ws://localhost:8080/custom",
		},
		{
			name:       "already wss URL",
			serverAddr: "wss://relay.example.com:8443/socks5",
			wsPath:     "/socks5",
			want:       "wss://relay.example.com:8443/socks5",
		},
		{
			name:       "http to ws",
			serverAddr: "http://localhost:8080",
			wsPath:     "/socks5",
			want:       "ws://localhost:8080/socks5",
		},
		{
			name:       "https to wss",
			serverAddr: "https://relay.example.com:8443",
			wsPath:     "/socks5",
			want:       "wss://relay.example.com:8443/socks5",
		},
		{
			name:       "host:port to wss",
			serverAddr: "relay.example.com:8443",
			wsPath:     "/socks5",
			want:       "wss://relay.example.com:8443/socks5",
		},
		{
			name:       "host:port with custom path",
			serverAddr: "proxy.internal:9000",
			wsPath:     "/ws/tunnel",
			want:       "wss://proxy.internal:9000/ws/tunnel",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{
				ServerAddr: tt.serverAddr,
				WSPath:     tt.wsPath,
			}
			got := client.buildWebSocketURL()
			if got != tt.want {
				t.Errorf("buildWebSocketURL() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewClientWithOptions(t *testing.T) {
	t.Run("default transport", func(t *testing.T) {
		client := NewClientWithOptions("127.0.0.1:1080", nil, 0, 0, ClientOptions{})
		if client.Transport != TransportTCP {
			t.Errorf("Transport = %v, want %v", client.Transport, TransportTCP)
		}
		if client.WSPath != "/socks5" {
			t.Errorf("WSPath = %v, want /socks5", client.WSPath)
		}
	})

	t.Run("websocket transport", func(t *testing.T) {
		client := NewClientWithOptions("proxy:8443", nil, 0, 0, ClientOptions{
			Transport: TransportWebSocket,
			WSPath:    "/tunnel",
		})
		if client.Transport != TransportWebSocket {
			t.Errorf("Transport = %v, want %v", client.Transport, TransportWebSocket)
		}
		if client.WSPath != "/tunnel" {
			t.Errorf("WSPath = %v, want /tunnel", client.WSPath)
		}
	})
}

// mockWebSocketServer creates a WebSocket server for testing
type mockWebSocketServer struct {
	server      *httptest.Server
	authMethod  byte
	authSuccess bool
}

func newMockWebSocketServer(t *testing.T) *mockWebSocketServer {
	t.Helper()

	mock := &mockWebSocketServer{
		authMethod:  AuthNone,
		authSuccess: true,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/socks5", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"socks5"},
		})
		if err != nil {
			t.Logf("websocket accept error: %v", err)
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")

		mock.handleSOCKS5(t, c)
	})

	mock.server = httptest.NewServer(mux)
	return mock
}

func (m *mockWebSocketServer) handleSOCKS5(t *testing.T, conn *websocket.Conn) {
	ctx := context.Background()

	// Read method selection
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Logf("read method selection error: %v", err)
		return
	}
	if len(data) < 2 || data[0] != Version {
		t.Logf("invalid method selection: %v", data)
		return
	}

	// Send method response
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{Version, m.authMethod}); err != nil {
		t.Logf("write method response error: %v", err)
		return
	}

	// Handle auth if needed
	if m.authMethod == AuthPassword {
		_, authData, err := conn.Read(ctx)
		if err != nil {
			return
		}
		// Parse auth request (version + ulen + username + plen + password)
		if len(authData) < 3 {
			return
		}

		status := byte(0x00)
		if !m.authSuccess {
			status = 0x01
		}
		if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01, status}); err != nil {
			return
		}
		if !m.authSuccess {
			return
		}
	}

	// Read connect request
	_, reqData, err := conn.Read(ctx)
	if err != nil {
		return
	}
	if len(reqData) < 4 {
		return
	}

	// Send reply with bound address (0.0.0.0:0)
	reply := []byte{
		Version,
		ReplySucceeded,
		0x00,         // reserved
		AddrTypeIPv4, // address type
		0, 0, 0, 0,   // IP
		0, 0, // port
	}
	if err := conn.Write(ctx, websocket.MessageBinary, reply); err != nil {
		return
	}

	// Echo loop
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if err := conn.Write(ctx, websocket.MessageBinary, data); err != nil {
			return
		}
	}
}

func (m *mockWebSocketServer) URL() string {
	return "ws" + m.server.URL[4:] + "/socks5" // http -> ws
}

func (m *mockWebSocketServer) Close() {
	m.server.Close()
}

func TestDialWebSocket(t *testing.T) {
	srv := newMockWebSocketServer(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := dialWebSocket(ctx, srv.URL(), 5*time.Second, "", "")
	if err != nil {
		t.Fatalf("dialWebSocket() error = %v", err)
	}
	defer conn.Close()

	// Verify it implements net.Conn
	if conn.LocalAddr() != nil {
		t.Log("LocalAddr() returned non-nil (expected)")
	}
	if conn.RemoteAddr() != nil {
		t.Log("RemoteAddr() returned non-nil (expected)")
	}
}

func TestWsConnReadWrite(t *testing.T) {
	srv := newMockWebSocketServer(t)
	defer srv.Close()

	// Create client that uses WebSocket
	client := NewClientWithOptions(srv.URL(), nil, 5*time.Second, 0, ClientOptions{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := client.Connect(ctx, "example.com:80")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer conn.Close()

	// Test write and read
	testData := []byte("hello websocket")
	n, err := conn.Write(testData)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if n != len(testData) {
		t.Errorf("Write() = %d, want %d", n, len(testData))
	}

	buf := make([]byte, len(testData))
	_, err = io.ReadFull(conn, buf)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if string(buf) != string(testData) {
		t.Errorf("got %q, want %q", buf, testData)
	}
}

func TestClientWebSocketWithAuth(t *testing.T) {
	srv := newMockWebSocketServer(t)
	srv.authMethod = AuthPassword
	srv.authSuccess = true
	defer srv.Close()

	auth := &UserPassAuth{Username: "user", Password: "pass"}
	client := NewClientWithOptions(srv.URL(), auth, 5*time.Second, 0, ClientOptions{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := client.Connect(ctx, "example.com:443")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	conn.Close()
}

func TestClientWebSocketAuthFailure(t *testing.T) {
	srv := newMockWebSocketServer(t)
	srv.authMethod = AuthPassword
	srv.authSuccess = false
	defer srv.Close()

	auth := &UserPassAuth{Username: "user", Password: "wrong"}
	client := NewClientWithOptions(srv.URL(), auth, 5*time.Second, 0, ClientOptions{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Connect(ctx, "example.com:80")
	if err == nil {
		t.Error("expected error for failed auth")
	}
}

func TestWsConnSetDeadline(t *testing.T) {
	srv := newMockWebSocketServer(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := dialWebSocket(ctx, srv.URL(), 5*time.Second, "", "")
	if err != nil {
		t.Fatalf("dialWebSocket() error = %v", err)
	}
	defer conn.Close()

	// Test SetDeadline
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Errorf("SetDeadline() error = %v", err)
	}

	// Test SetReadDeadline
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Errorf("SetReadDeadline() error = %v", err)
	}

	// Test SetWriteDeadline
	if err := conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Errorf("SetWriteDeadline() error = %v", err)
	}

	// Test clearing deadline
	if err := conn.SetDeadline(time.Time{}); err != nil {
		t.Errorf("SetDeadline(zero) error = %v", err)
	}
}

func TestDialWebSocketTimeout(t *testing.T) {
	// Use an address that won't connect
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := dialWebSocket(ctx, "ws://192.0.2.1:12345/socks5", 100*time.Millisecond, "", "")
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestDialWebSocketInvalidURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := dialWebSocket(ctx, "not-a-url", time.Second, "", "")
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

// mockWebSocketServerWithHTTPAuth creates a WebSocket server that requires HTTP Basic Auth
type mockWebSocketServerWithHTTPAuth struct {
	server   *httptest.Server
	username string
	password string
}

func newMockWebSocketServerWithHTTPAuth(t *testing.T, username, password string) *mockWebSocketServerWithHTTPAuth {
	t.Helper()

	mock := &mockWebSocketServerWithHTTPAuth{
		username: username,
		password: password,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/socks5", func(w http.ResponseWriter, r *http.Request) {
		// Check HTTP Basic Auth
		user, pass, ok := r.BasicAuth()
		if !ok || user != mock.username || pass != mock.password {
			w.Header().Set("WWW-Authenticate", `Basic realm="SOCKS5"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"socks5"},
		})
		if err != nil {
			t.Logf("websocket accept error: %v", err)
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")

		// Simple echo loop
		for {
			_, data, err := c.Read(context.Background())
			if err != nil {
				return
			}
			if err := c.Write(context.Background(), websocket.MessageBinary, data); err != nil {
				return
			}
		}
	})

	mock.server = httptest.NewServer(mux)
	return mock
}

func (m *mockWebSocketServerWithHTTPAuth) URL() string {
	return "ws" + m.server.URL[4:] + "/socks5"
}

func (m *mockWebSocketServerWithHTTPAuth) Close() {
	m.server.Close()
}

func TestDialWebSocketWithHTTPBasicAuth(t *testing.T) {
	srv := newMockWebSocketServerWithHTTPAuth(t, "testuser", "testpass")
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Test with correct credentials
	conn, err := dialWebSocket(ctx, srv.URL(), 5*time.Second, "testuser", "testpass")
	if err != nil {
		t.Fatalf("dialWebSocket() with correct credentials error = %v", err)
	}
	conn.Close()
}

func TestDialWebSocketWithHTTPBasicAuthWrongCredentials(t *testing.T) {
	srv := newMockWebSocketServerWithHTTPAuth(t, "testuser", "testpass")
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Test with wrong credentials
	_, err := dialWebSocket(ctx, srv.URL(), 5*time.Second, "wrong", "wrong")
	if err == nil {
		t.Error("expected error for wrong credentials")
	}
}

func TestDialWebSocketWithHTTPBasicAuthNoCredentials(t *testing.T) {
	srv := newMockWebSocketServerWithHTTPAuth(t, "testuser", "testpass")
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Test with no credentials (server requires them)
	_, err := dialWebSocket(ctx, srv.URL(), 5*time.Second, "", "")
	if err == nil {
		t.Error("expected error for missing credentials")
	}
}

func TestClientWithWSCredentials(t *testing.T) {
	t.Run("credentials are stored", func(t *testing.T) {
		client := NewClientWithOptions("proxy:8443", nil, 0, 0, ClientOptions{
			Transport:  TransportWebSocket,
			WSPath:     "/socks5",
			WSUsername: "user",
			WSPassword: "pass",
		})
		if client.WSUsername != "user" {
			t.Errorf("WSUsername = %v, want %v", client.WSUsername, "user")
		}
		if client.WSPassword != "pass" {
			t.Errorf("WSPassword = %v, want %v", client.WSPassword, "pass")
		}
	})
}

// mockWebSocketServerWithHTTPAuthAndSOCKS5 creates a server requiring both HTTP Basic Auth and SOCKS5 auth
type mockWebSocketServerWithHTTPAuthAndSOCKS5 struct {
	server       *httptest.Server
	httpUsername string
	httpPassword string
	socksAuth    bool
}

func newMockWebSocketServerWithHTTPAuthAndSOCKS5(t *testing.T, httpUser, httpPass string, requireSOCKSAuth bool) *mockWebSocketServerWithHTTPAuthAndSOCKS5 {
	t.Helper()

	mock := &mockWebSocketServerWithHTTPAuthAndSOCKS5{
		httpUsername: httpUser,
		httpPassword: httpPass,
		socksAuth:    requireSOCKSAuth,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/socks5", func(w http.ResponseWriter, r *http.Request) {
		// Check HTTP Basic Auth
		user, pass, ok := r.BasicAuth()
		if !ok || user != mock.httpUsername || pass != mock.httpPassword {
			w.Header().Set("WWW-Authenticate", `Basic realm="SOCKS5"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"socks5"},
		})
		if err != nil {
			t.Logf("websocket accept error: %v", err)
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")

		mock.handleSOCKS5(t, c)
	})

	mock.server = httptest.NewServer(mux)
	return mock
}

func (m *mockWebSocketServerWithHTTPAuthAndSOCKS5) handleSOCKS5(t *testing.T, conn *websocket.Conn) {
	ctx := context.Background()

	// Read method selection
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Logf("read method selection error: %v", err)
		return
	}
	if len(data) < 2 || data[0] != Version {
		t.Logf("invalid method selection: %v", data)
		return
	}

	// Determine auth method
	authMethod := byte(AuthNone)
	if m.socksAuth {
		authMethod = AuthPassword
	}

	// Send method response
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{Version, authMethod}); err != nil {
		t.Logf("write method response error: %v", err)
		return
	}

	// Handle SOCKS5 auth if needed
	if m.socksAuth {
		_, authData, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if len(authData) < 3 {
			return
		}
		// Accept any SOCKS5 credentials for this test
		if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01, 0x00}); err != nil {
			return
		}
	}

	// Read connect request
	_, reqData, err := conn.Read(ctx)
	if err != nil {
		return
	}
	if len(reqData) < 4 {
		return
	}

	// Send reply with bound address (0.0.0.0:0)
	reply := []byte{
		Version,
		ReplySucceeded,
		0x00,         // reserved
		AddrTypeIPv4, // address type
		0, 0, 0, 0,   // IP
		0, 0, // port
	}
	if err := conn.Write(ctx, websocket.MessageBinary, reply); err != nil {
		return
	}

	// Echo loop
	for {
		_, echoData, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if err := conn.Write(ctx, websocket.MessageBinary, echoData); err != nil {
			return
		}
	}
}

func (m *mockWebSocketServerWithHTTPAuthAndSOCKS5) URL() string {
	return "ws" + m.server.URL[4:] + "/socks5"
}

func (m *mockWebSocketServerWithHTTPAuthAndSOCKS5) Close() {
	m.server.Close()
}

func TestClientConnectWithHTTPBasicAuth(t *testing.T) {
	srv := newMockWebSocketServerWithHTTPAuthAndSOCKS5(t, "httpuser", "httppass", false)
	defer srv.Close()

	// Create client with HTTP Basic Auth credentials
	client := NewClientWithOptions(srv.URL(), nil, 5*time.Second, 0, ClientOptions{
		WSUsername: "httpuser",
		WSPassword: "httppass",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := client.Connect(ctx, "example.com:80")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer conn.Close()

	// Test data round-trip
	testData := []byte("hello via http basic auth")
	if _, err := conn.Write(testData); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	buf := make([]byte, len(testData))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if string(buf) != string(testData) {
		t.Errorf("got %q, want %q", buf, testData)
	}
}

func TestClientConnectWithHTTPBasicAuthAndSOCKS5Auth(t *testing.T) {
	srv := newMockWebSocketServerWithHTTPAuthAndSOCKS5(t, "httpuser", "httppass", true)
	defer srv.Close()

	// Create client with both HTTP Basic Auth and SOCKS5 auth
	socksAuth := &UserPassAuth{Username: "socksuser", Password: "sockspass"}
	client := NewClientWithOptions(srv.URL(), socksAuth, 5*time.Second, 0, ClientOptions{
		WSUsername: "httpuser",
		WSPassword: "httppass",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := client.Connect(ctx, "example.com:443")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer conn.Close()

	// Test data round-trip
	testData := []byte("hello via dual auth")
	if _, err := conn.Write(testData); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	buf := make([]byte, len(testData))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if string(buf) != string(testData) {
		t.Errorf("got %q, want %q", buf, testData)
	}
}

func TestClientConnectWithWrongHTTPBasicAuth(t *testing.T) {
	srv := newMockWebSocketServerWithHTTPAuthAndSOCKS5(t, "httpuser", "httppass", false)
	defer srv.Close()

	// Create client with wrong HTTP Basic Auth credentials
	client := NewClientWithOptions(srv.URL(), nil, 5*time.Second, 0, ClientOptions{
		WSUsername: "wrong",
		WSPassword: "wrong",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Connect(ctx, "example.com:80")
	if err == nil {
		t.Error("expected error for wrong HTTP Basic Auth credentials")
	}
}
