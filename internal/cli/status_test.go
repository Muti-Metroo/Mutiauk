package cli

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"github.com/Muti-Metroo/Mutiauk/internal/api"
	"github.com/Muti-Metroo/Mutiauk/internal/config"
	"github.com/Muti-Metroo/Mutiauk/internal/socks5"
)

// wsSOCKS5MockServer answers the SOCKS5 protocol over WebSocket: each
// connection gets a no-auth method reply followed by a success reply for
// whatever request (CONNECT, UDP ASSOCIATE) arrives next.
func wsSOCKS5MockServer(t *testing.T) *httptest.Server {
	t.Helper()

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

		ctx := context.Background()
		for step := 0; ; step++ {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if len(data) < 2 || data[0] != socks5.Version {
				return
			}
			var reply []byte
			if step == 0 {
				reply = []byte{socks5.Version, socks5.AuthNone}
			} else {
				reply = []byte{
					socks5.Version,
					socks5.ReplySucceeded,
					0x00,
					socks5.AddrTypeIPv4,
					0, 0, 0, 0,
					0, 0,
				}
			}
			if err := c.Write(ctx, websocket.MessageBinary, reply); err != nil {
				return
			}
		}
	})

	return httptest.NewServer(mux)
}

// TestRunConnectivityTests_WebSocketEndpoint is the regression guard for the
// bogus "[FAIL] ... too many colons in address" report: the reachability
// probe used to raw-dial the configured endpoint, which breaks when the
// endpoint is a ws:// URL. The probe must go through the same WebSocket
// upgrade the data path uses.
func TestRunConnectivityTests_WebSocketEndpoint(t *testing.T) {
	srv := wsSOCKS5MockServer(t)
	defer srv.Close()

	cfg := &config.Config{}
	cfg.SOCKS5.Server = "ws" + srv.URL[len("http"):] + "/socks5"
	cfg.SOCKS5.Timeout = 5 * time.Second

	status := runConnectivityTests(cfg)

	if !status.SOCKS5Reachable {
		t.Fatalf("SOCKS5Reachable = false, want true (error: %q)", status.SOCKS5Error)
	}
	if status.SOCKS5Latency == "" {
		t.Error("SOCKS5Latency is empty, want a measured latency")
	}
	if !status.TCPRouting {
		t.Errorf("TCPRouting = false, want true (error: %q)", status.TCPError)
	}
	if !status.UDPRouting {
		t.Errorf("UDPRouting = false, want true (error: %q)", status.UDPError)
	}
}

// TestRunConnectivityTests_PlainTCPEndpoint locks in the unchanged behavior
// for plain host:port endpoints: the reachability probe still succeeds with
// a raw TCP dial, even when the listener does not speak SOCKS5.
func TestRunConnectivityTests_PlainTCPEndpoint(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	cfg := &config.Config{}
	cfg.SOCKS5.Server = ln.Addr().String()
	cfg.SOCKS5.Timeout = 300 * time.Millisecond

	status := runConnectivityTests(cfg)

	if !status.SOCKS5Reachable {
		t.Fatalf("SOCKS5Reachable = false, want true (error: %q)", status.SOCKS5Error)
	}
	if status.TCPRouting {
		t.Error("TCPRouting = true, want false (listener does not speak SOCKS5)")
	}
}

// TestCheckTUNStatus_DaemonUp is the regression guard for the bug where status
// reported the TUN interface as "only available on Linux" / exists:false off
// Linux. When the daemon is up, the real kernel-assigned name from the API must
// be reported on every platform (e.g. utun10 on macOS), not the configured one.
func TestCheckTUNStatus_DaemonUp(t *testing.T) {
	cfg := &config.Config{}
	cfg.TUN.Name = "mutiauk" // configured name; must NOT be what we report
	cfg.TUN.Address = "ignored"

	apiStatus := &api.StatusResult{
		TUNName:    "utun10",
		TUNAddress: "10.200.200.1/24",
	}

	got := checkTUNStatus(cfg, apiStatus)

	if !got.Exists {
		t.Errorf("Exists = false, want true (daemon is up with a live TUN)")
	}
	if got.Name != "utun10" {
		t.Errorf("Name = %q, want %q (real kernel name, not configured tun.name)", got.Name, "utun10")
	}
	if got.Address != "10.200.200.1/24" {
		t.Errorf("Address = %q, want %q", got.Address, "10.200.200.1/24")
	}
	if got.Message != "up" {
		t.Errorf("Message = %q, want %q", got.Message, "up")
	}
}

// TestCheckTUNStatus_DaemonDown verifies the offline path never claims the
// interface is "only available on Linux", and reports an honest state.
func TestCheckTUNStatus_DaemonDown(t *testing.T) {
	cfg := &config.Config{}
	cfg.TUN.Name = "mutiauk"

	got := checkTUNStatus(cfg, nil)

	if got.Message == "TUN interface only available on Linux" {
		t.Errorf("Message = %q; the Linux-only wording must be gone", got.Message)
	}

	if runtime.GOOS != "linux" {
		// Off Linux the daemon owns the device, so with no daemon there is no
		// TUN to introspect: report it as absent and not running.
		if got.Exists {
			t.Errorf("Exists = true, want false (no daemon, no TUN off Linux)")
		}
		if got.Message != "daemon not running" {
			t.Errorf("Message = %q, want %q", got.Message, "daemon not running")
		}
		if got.Name != "mutiauk" {
			t.Errorf("Name = %q, want configured name %q", got.Name, "mutiauk")
		}
	}
}
