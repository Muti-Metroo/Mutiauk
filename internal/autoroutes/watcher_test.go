package autoroutes

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/postalsys/mutiauk/internal/route"
	"github.com/postalsys/mutiauk/internal/socks5"
	"go.uber.org/zap"
)

// fakeSOCKSServer is a minimal SOCKS5 server for watcher tests: no-auth
// greeting, one request, a reply, then optional frame streaming.
func fakeSOCKSServer(t *testing.T, replyCode byte, stream func(conn net.Conn)) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				hdr := make([]byte, 2)
				if _, err := io.ReadFull(conn, hdr); err != nil {
					conn.Close()
					return
				}
				io.ReadFull(conn, make([]byte, int(hdr[1])))
				conn.Write([]byte{0x05, 0x00}) // no-auth

				if _, err := io.ReadFull(conn, make([]byte, 10)); err != nil {
					conn.Close()
					return
				}
				conn.Write([]byte{0x05, replyCode, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
				if replyCode != 0x00 {
					conn.Close()
					return
				}
				if stream != nil {
					stream(conn)
				}
			}(conn)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln
}

func frameBytes(frameType byte, payload []byte) []byte {
	buf := make([]byte, 5+len(payload))
	buf[0] = frameType
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(payload)))
	copy(buf[5:], payload)
	return buf
}

func TestWatcher_AppliesSnapshot(t *testing.T) {
	const snap = `{"routes":[{"network":"10.0.0.0/8","route_type":"cidr","origin_id":"abc123"}]}`

	ln := fakeSOCKSServer(t, 0x00, func(conn net.Conn) {
		conn.Write(frameBytes(1, []byte(snap))) // SNAPSHOT
		time.Sleep(200 * time.Millisecond)
		conn.Close()
	})

	client := socks5.NewClient(ln.Addr().String(), nil, 2*time.Second, 0)

	updates := make(chan []route.Route, 1)
	cb := func(routes []route.Route, suffixes []string, hosts []DashboardMeshHost) {
		select {
		case updates <- routes:
		default:
		}
	}

	w := NewWatcher(client, "tun-test", cb, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	select {
	case routes := <-updates:
		if len(routes) != 1 || routes[0].Destination.String() != "10.0.0.0/8" {
			t.Fatalf("got routes %+v, want one 10.0.0.0/8", routes)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for snapshot to be applied")
	}
}

func TestWatcher_LastSnapshotTimestamp(t *testing.T) {
	w := NewWatcher(nil, "tun-test", nil, zap.NewNop())
	if !w.LastSnapshot().IsZero() {
		t.Fatal("LastSnapshot should be zero before any snapshot")
	}

	before := time.Now()
	if err := w.apply([]byte(`{"routes":[]}`)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := w.LastSnapshot()
	if got.Before(before) || got.After(time.Now()) {
		t.Fatalf("LastSnapshot = %v, want within [%v, now]", got, before)
	}
}

func TestWatcher_UnsupportedReturnsErr(t *testing.T) {
	ln := fakeSOCKSServer(t, 0x07, nil) // command not supported

	client := socks5.NewClient(ln.Addr().String(), nil, 2*time.Second, 0)
	w := NewWatcher(client, "tun-test", nil, zap.NewNop())

	done := make(chan error, 1)
	go func() { done <- w.Run(context.Background()) }()

	select {
	case err := <-done:
		if err != ErrUnsupported {
			t.Fatalf("Run returned %v, want ErrUnsupported", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for ErrUnsupported")
	}
}
