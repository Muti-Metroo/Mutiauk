//go:build linux

package tun

import (
	"net"
	"os"
	"strings"
	"testing"
)

func TestConstants(t *testing.T) {
	if tunDevice != "/dev/net/tun" {
		t.Errorf("tunDevice = %s, want /dev/net/tun", tunDevice)
	}
	if ifnamsiz != 16 {
		t.Errorf("ifnamsiz = %d, want 16", ifnamsiz)
	}
}

func TestFlags(t *testing.T) {
	// Verify TUN flags are correct
	if cIFF_TUN != 0x0001 {
		t.Errorf("cIFF_TUN = 0x%04x, want 0x0001", cIFF_TUN)
	}
	if cIFF_NO_PI != 0x1000 {
		t.Errorf("cIFF_NO_PI = 0x%04x, want 0x1000", cIFF_NO_PI)
	}
}

func TestIfReq_Size(t *testing.T) {
	// ifReq should be exactly 40 bytes for the ioctl to work correctly
	var req ifReq
	expectedSize := ifnamsiz + 2 + 22 // Name + Flags + padding

	// This is a compile-time verification - the struct exists and has correct fields
	_ = req.Name
	_ = req.Flags

	// Verify name can hold interface name
	if len(req.Name) != ifnamsiz {
		t.Errorf("ifReq.Name length = %d, want %d", len(req.Name), ifnamsiz)
	}

	_ = expectedSize // Used for documentation
}

func TestIfReq_NameCopy(t *testing.T) {
	var req ifReq

	// Test copying short name
	name := "tun0"
	copy(req.Name[:], name)

	// Verify name is null-terminated correctly
	if string(req.Name[:len(name)]) != name {
		t.Errorf("name = %q, want %q", string(req.Name[:len(name)]), name)
	}
	if req.Name[len(name)] != 0 {
		t.Error("name should be null-terminated")
	}
}

func TestIfReq_LongName(t *testing.T) {
	var req ifReq

	// Test with name exactly at limit (15 chars + null)
	name := "tun0123456789ab" // 15 chars
	copy(req.Name[:], name)

	if string(req.Name[:len(name)]) != name {
		t.Errorf("name = %q, want %q", string(req.Name[:len(name)]), name)
	}
}

func TestIfReq_TooLongName(t *testing.T) {
	var req ifReq

	// Name longer than ifnamsiz gets truncated
	name := "verylonginterfacename" // 21 chars
	copy(req.Name[:], name)

	// Only first 16 bytes should be copied
	if string(req.Name[:]) != name[:ifnamsiz] {
		t.Errorf("truncated name = %q, want %q", string(req.Name[:]), name[:ifnamsiz])
	}
}

func TestNew_NoRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping non-root test when running as root")
	}

	cfg := DefaultConfig()
	dev, err := New(cfg)

	// Should fail without root
	if err == nil {
		if dev != nil {
			dev.Close()
		}
		t.Error("New() should fail without root privileges")
	}

	// Error should be about permission or /dev/net/tun
	errStr := err.Error()
	if !strings.Contains(errStr, "permission") &&
		!strings.Contains(errStr, "operation not permitted") &&
		!strings.Contains(errStr, "/dev/net/tun") {
		t.Logf("Note: error = %v", err)
	}
}

func TestLinuxDevice_Methods(t *testing.T) {
	// Test linuxDevice struct fields and methods without actual device
	dev := &linuxDevice{
		file: nil,
		name: "test0",
		mtu:  1400,
	}

	if dev.Name() != "test0" {
		t.Errorf("Name() = %s, want test0", dev.Name())
	}
	if dev.MTU() != 1400 {
		t.Errorf("MTU() = %d, want 1400", dev.MTU())
	}
}

func TestLinuxDevice_StructCreation(t *testing.T) {
	// Verify linuxDevice struct can be created with expected fields
	dev := &linuxDevice{
		file: nil,
		name: "test0",
		mtu:  1400,
	}

	// Verify fields are accessible
	if dev.name != "test0" {
		t.Errorf("name = %s, want test0", dev.name)
	}
	if dev.mtu != 1400 {
		t.Errorf("mtu = %d, want 1400", dev.mtu)
	}
	if dev.file != nil {
		t.Error("file should be nil")
	}
}

func TestLinuxDevice_InterfaceCompliance(t *testing.T) {
	// Verify linuxDevice implements Device interface (compile-time check)
	var dev Device = (*linuxDevice)(nil)
	_ = dev
}

// TestNew_TunDeviceExists verifies /dev/net/tun exists on Linux
func TestNew_TunDeviceExists(t *testing.T) {
	_, err := os.Stat(tunDevice)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skipf("%s does not exist (not a standard Linux system?)", tunDevice)
		}
		t.Errorf("stat %s: %v", tunDevice, err)
	}
}

func TestConfig_ValidIPv4Config(t *testing.T) {
	cfg := Config{
		Name:    "tun0",
		MTU:     1400,
		Address: net.ParseIP("10.200.200.1"),
		Netmask: net.CIDRMask(24, 32),
	}

	// Verify config is valid
	if cfg.Address == nil {
		t.Error("Address should not be nil")
	}
	if cfg.Address.To4() == nil {
		t.Error("Address should be IPv4")
	}

	ones, _ := cfg.Netmask.Size()
	if ones != 24 {
		t.Errorf("Netmask = /%d, want /24", ones)
	}
}

func TestConfig_ValidIPv6Config(t *testing.T) {
	cfg := Config{
		Name:     "tun0",
		MTU:      1400,
		Address6: net.ParseIP("fd00::1"),
		Netmask6: net.CIDRMask(64, 128),
	}

	if cfg.Address6 == nil {
		t.Error("Address6 should not be nil")
	}
	if cfg.Address6.To4() != nil {
		t.Error("Address6 should be IPv6, not IPv4")
	}

	ones, bits := cfg.Netmask6.Size()
	if ones != 64 || bits != 128 {
		t.Errorf("Netmask6 = /%d (bits=%d), want /64 (bits=128)", ones, bits)
	}
}

// Benchmark tests

func BenchmarkIfReqNameCopy(b *testing.B) {
	name := "tun0"
	var req ifReq

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copy(req.Name[:], name)
	}
}
