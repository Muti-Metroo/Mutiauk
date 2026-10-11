package stack

import (
	"net"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gvisor.dev/gvisor/pkg/tcpip/header"
)

// mockTunDevice implements tun.Device and records writes.
type mockTunDevice struct {
	mu     sync.Mutex
	writes [][]byte
}

func (m *mockTunDevice) Read(p []byte) (int, error) {
	return 0, net.ErrClosed
}

func (m *mockTunDevice) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes = append(m.writes, append([]byte(nil), p...))
	return len(p), nil
}

func (m *mockTunDevice) Close() error         { return nil }
func (m *mockTunDevice) Name() string         { return "mock0" }
func (m *mockTunDevice) MTU() int             { return 1400 }
func (m *mockTunDevice) SetMTU(mtu int) error { return nil }
func (m *mockTunDevice) File() int            { return -1 }

func (m *mockTunDevice) lastWrite() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.writes) == 0 {
		return nil
	}
	return m.writes[len(m.writes)-1]
}

func newTestStackForWrites(mtu int) (*Stack, *mockTunDevice, *observer.ObservedLogs) {
	core, logs := observer.New(zap.WarnLevel)
	dev := &mockTunDevice{}
	s := &Stack{
		tunDev:   dev,
		cfg:      Config{MTU: mtu},
		logger:   zap.New(core),
		truncLog: newLogRateLimiter(time.Minute),
	}
	return s, dev, logs
}

func TestClampUDPPayload(t *testing.T) {
	tests := []struct {
		name      string
		payload   int
		mtu       int
		v6        bool
		wantLen   int
		truncated bool
	}{
		{"v4 under mtu", 100, 1400, false, 100, false},
		{"v4 exactly mtu", 1372, 1400, false, 1372, false},
		{"v4 over mtu", 2000, 1400, false, 1372, true},
		{"v6 under mtu", 100, 1400, true, 100, false},
		{"v6 exactly mtu", 1352, 1400, true, 1352, false},
		{"v6 over mtu", 2000, 1400, true, 1352, true},
		{"tiny mtu clamps to zero", 100, 20, false, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := make([]byte, tt.payload)
			got, truncated := clampUDPPayload(payload, tt.mtu, tt.v6)
			if truncated != tt.truncated {
				t.Errorf("truncated = %v, want %v", truncated, tt.truncated)
			}
			if len(got) != tt.wantLen {
				t.Errorf("len = %d, want %d", len(got), tt.wantLen)
			}
		})
	}
}

func TestWriteUDPResponse_UnderMTU(t *testing.T) {
	s, dev, logs := newTestStackForWrites(1400)

	payload := make([]byte, 100)
	s.writeUDPResponse(net.ParseIP("8.8.8.8"), net.ParseIP("10.200.200.2"), 53, 5300, payload)

	pkt := dev.lastWrite()
	if pkt == nil {
		t.Fatal("no packet written")
	}
	wantLen := header.IPv4MinimumSize + header.UDPMinimumSize + 100
	if len(pkt) != wantLen {
		t.Errorf("packet length = %d, want %d", len(pkt), wantLen)
	}
	if logs.Len() != 0 {
		t.Errorf("unexpected warnings logged: %v", logs.All())
	}
}

func TestWriteUDPResponse_TruncatesToMTU(t *testing.T) {
	s, dev, logs := newTestStackForWrites(1400)

	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = byte(i % 256)
	}
	s.writeUDPResponse(net.ParseIP("8.8.8.8"), net.ParseIP("10.200.200.2"), 53, 5300, payload)

	pkt := dev.lastWrite()
	if pkt == nil {
		t.Fatal("no packet written")
	}

	// The packet must fit the link MTU exactly.
	if len(pkt) != 1400 {
		t.Errorf("packet length = %d, want 1400 (MTU)", len(pkt))
	}

	ipHdr := header.IPv4(pkt)
	if int(ipHdr.TotalLength()) != 1400 {
		t.Errorf("IP total length = %d, want 1400", ipHdr.TotalLength())
	}

	udpHdr := header.UDP(pkt[header.IPv4MinimumSize:])
	wantUDPLen := 1400 - header.IPv4MinimumSize
	if int(udpHdr.Length()) != wantUDPLen {
		t.Errorf("UDP length = %d, want %d", udpHdr.Length(), wantUDPLen)
	}

	// Truncated payload must be a prefix of the original.
	udpPayload := pkt[header.IPv4MinimumSize+header.UDPMinimumSize:]
	for i := 0; i < len(udpPayload); i++ {
		if udpPayload[i] != byte(i%256) {
			t.Fatalf("payload mismatch at %d: got %d, want %d", i, udpPayload[i], byte(i%256))
		}
	}

	if logs.Len() != 1 {
		t.Fatalf("expected 1 truncation warning, got %d", logs.Len())
	}
}

func TestWriteUDPResponse_TruncationLogRateLimited(t *testing.T) {
	s, dev, _ := newTestStackForWrites(1400)

	payload := make([]byte, 4096)
	s.writeUDPResponse(net.ParseIP("8.8.8.8"), net.ParseIP("10.200.200.2"), 53, 5300, payload)

	// Second truncation within the interval must be counted, not logged.
	core, logs := observer.New(zap.WarnLevel)
	s.logger = zap.New(core)
	s.writeUDPResponse(net.ParseIP("8.8.8.8"), net.ParseIP("10.200.200.2"), 53, 5300, payload)

	if logs.Len() != 0 {
		t.Errorf("expected rate-limited truncation log, got %d entries", logs.Len())
	}
	if s.truncLog.suppressed != 1 {
		t.Errorf("suppressed = %d, want 1", s.truncLog.suppressed)
	}
	_ = dev
}

func TestWriteUDPResponse_IPv6TruncatesToMTU(t *testing.T) {
	s, dev, _ := newTestStackForWrites(1400)

	payload := make([]byte, 4096)
	s.writeUDPResponse(net.ParseIP("2001:db8::1"), net.ParseIP("fd00::2"), 53, 5300, payload)

	pkt := dev.lastWrite()
	if pkt == nil {
		t.Fatal("no packet written")
	}
	if len(pkt) != 1400 {
		t.Errorf("packet length = %d, want 1400 (MTU)", len(pkt))
	}
	if version := pkt[0] >> 4; version != 6 {
		t.Errorf("IP version = %d, want 6", version)
	}
}
