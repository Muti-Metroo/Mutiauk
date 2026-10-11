//go:build darwin

package stack

import (
	"testing"

	"github.com/Muti-Metroo/Mutiauk/internal/tun"
	"go.uber.org/zap"
	"gvisor.dev/gvisor/pkg/tcpip/header"
)

func TestProtocolNumber(t *testing.T) {
	tests := []struct {
		name  string
		first byte
		want  uint32
	}{
		{"ipv4", 0x45, uint32(header.IPv4ProtocolNumber)},
		{"ipv6", 0x60, uint32(header.IPv6ProtocolNumber)},
		{"unknown version 5", 0x50, 0},
		{"zero", 0x00, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := uint32(protocolNumber(tt.first)); got != tt.want {
				t.Errorf("protocolNumber(0x%02x) = %d, want %d", tt.first, got, tt.want)
			}
		})
	}
}

// fakeDevice is a minimal tun.Device for constructing the endpoint without a
// real utun interface.
type fakeDevice struct{ tun.Device }

func (fakeDevice) MTU() int { return 1400 }

func TestNewBaseLinkEndpoint_Darwin(t *testing.T) {
	ep, starter, err := newBaseLinkEndpoint(fakeDevice{}, 1400, zap.NewNop())
	if err != nil {
		t.Fatalf("newBaseLinkEndpoint: %v", err)
	}
	if ep == nil {
		t.Error("expected a non-nil link endpoint")
	}
	if _, ok := starter.(*darwinPump); !ok {
		t.Errorf("expected a *darwinPump starter, got %T", starter)
	}
	if mtu := ep.MTU(); mtu != 1400 {
		t.Errorf("endpoint MTU = %d, want 1400", mtu)
	}
}
