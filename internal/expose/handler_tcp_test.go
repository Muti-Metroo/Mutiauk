package expose

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestTCPHandlerEchoesBytes(t *testing.T) {
	// Stand up an echo server on a random port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()

	h := newTCPHandler(ln.Addr().String(), zap.NewNop())

	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.Serve(ctx, serverSide)
	}()

	payload := []byte("ping-pong")
	if _, err := clientSide.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(payload))
	_ = clientSide.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(clientSide, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != string(payload) {
		t.Errorf("echo mismatch: got %q want %q", buf, payload)
	}
	clientSide.Close()
	wg.Wait()
}

func TestTCPHandlerDialFailureReturnsError(t *testing.T) {
	// Port 1 is reserved + privileged; dial should fail fast.
	h := newTCPHandler("127.0.0.1:1", zap.NewNop())
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := h.Serve(ctx, serverSide)
	if err == nil {
		t.Error("expected dial error, got nil")
	}
}
