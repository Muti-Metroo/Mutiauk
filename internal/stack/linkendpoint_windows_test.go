//go:build windows

package stack

import (
	"testing"

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
		{"garbage", 0x00, 0},
		{"high nibble 7", 0x70, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := uint32(protocolNumber(tt.first)); got != tt.want {
				t.Errorf("protocolNumber(%#x) = %d, want %d", tt.first, got, tt.want)
			}
		})
	}
}
