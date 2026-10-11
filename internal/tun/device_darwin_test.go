//go:build darwin

package tun

import (
	"bytes"
	"encoding/binary"
	"net"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestUtunUnit(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    uint32
		wantErr bool
	}{
		{"empty means auto", "", 0, false},
		{"cross-platform default means auto", "tun0", 0, false},
		{"bare utun means auto", "utun", 0, false},
		{"utun0 -> unit 1", "utun0", 1, false},
		{"utun7 -> unit 8", "utun7", 8, false},
		{"utun12 -> unit 13", "utun12", 13, false},
		{"non-numeric suffix", "utunX", 0, true},
		{"negative", "utun-1", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := utunUnit(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("utunUnit(%q) = %d, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("utunUnit(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("utunUnit(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestDarwinDevice_ReadStripsAFHeader(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	dev := &darwinDevice{file: r, mtu: 1400, rbuf: make([]byte, 1400+afHeaderLen)}

	ipPacket := []byte{0x45, 0x00, 0xDE, 0xAD, 0xBE, 0xEF} // arbitrary IPv4-looking bytes
	framed := make([]byte, afHeaderLen+len(ipPacket))
	binary.BigEndian.PutUint32(framed[:afHeaderLen], unix.AF_INET)
	copy(framed[afHeaderLen:], ipPacket)

	if _, err := w.Write(framed); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 100)
	n, err := dev.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if n != len(ipPacket) {
		t.Fatalf("Read returned n=%d, want %d", n, len(ipPacket))
	}
	if !bytes.Equal(buf[:n], ipPacket) {
		t.Errorf("Read payload = %x, want %x (AF header not stripped?)", buf[:n], ipPacket)
	}
}

func TestDarwinDevice_ReadRuntPacket(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	dev := &darwinDevice{file: r, mtu: 1400, rbuf: make([]byte, 1400+afHeaderLen)}

	// Fewer bytes than the AF header: should be reported as a zero-length read,
	// not an out-of-range slice.
	if _, err := w.Write([]byte{0x00, 0x00}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 100)
	n, err := dev.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if n != 0 {
		t.Errorf("Read of runt packet returned n=%d, want 0", n)
	}
}

func TestDarwinDevice_WritePrependsAFHeader(t *testing.T) {
	tests := []struct {
		name   string
		packet []byte
		wantAF uint32
	}{
		{"ipv4", []byte{0x45, 0x01, 0x02, 0x03}, unix.AF_INET},
		{"ipv6", []byte{0x60, 0x0a, 0x0b, 0x0c}, unix.AF_INET6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close()

			dev := &darwinDevice{file: w, mtu: 1400}

			n, err := dev.Write(tt.packet)
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			if n != len(tt.packet) {
				t.Errorf("Write returned n=%d, want %d (should exclude AF header)", n, len(tt.packet))
			}

			got := make([]byte, afHeaderLen+len(tt.packet))
			if _, err := r.Read(got); err != nil {
				t.Fatal(err)
			}
			if af := binary.BigEndian.Uint32(got[:afHeaderLen]); af != tt.wantAF {
				t.Errorf("AF header = %d, want %d", af, tt.wantAF)
			}
			if !bytes.Equal(got[afHeaderLen:], tt.packet) {
				t.Errorf("framed payload = %x, want %x", got[afHeaderLen:], tt.packet)
			}
		})
	}
}

func TestDarwinDevice_WriteEmpty(t *testing.T) {
	dev := &darwinDevice{mtu: 1400} // file unused; empty write must short-circuit
	n, err := dev.Write(nil)
	if err != nil || n != 0 {
		t.Errorf("Write(nil) = (%d, %v), want (0, nil)", n, err)
	}
}

// TestNew_RealUtun exercises the real utun open/configure/close path. It needs
// root and is skipped otherwise.
func TestNew_RealUtun(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to create a utun device")
	}

	cfg := Config{
		MTU:     1400,
		Address: net.ParseIP("10.200.201.1"),
		Netmask: net.CIDRMask(24, 32),
	}
	dev, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer dev.Close()

	if !strings.HasPrefix(dev.Name(), "utun") {
		t.Errorf("Name = %q, want utunN", dev.Name())
	}
	if dev.MTU() != 1400 {
		t.Errorf("MTU = %d, want 1400", dev.MTU())
	}
	if dev.File() <= 0 {
		t.Errorf("File = %d, want a valid fd", dev.File())
	}

	// The interface should now exist.
	if _, err := net.InterfaceByName(dev.Name()); err != nil {
		t.Errorf("InterfaceByName(%q): %v", dev.Name(), err)
	}
}
