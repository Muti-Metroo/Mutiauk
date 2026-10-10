package tun

import (
	"net"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Name != "tun0" {
		t.Errorf("Name = %s, want tun0", cfg.Name)
	}
	if cfg.MTU != 1400 {
		t.Errorf("MTU = %d, want 1400", cfg.MTU)
	}
	if !cfg.Address.Equal(net.ParseIP("10.200.200.1")) {
		t.Errorf("Address = %v, want 10.200.200.1", cfg.Address)
	}

	ones, bits := cfg.Netmask.Size()
	if ones != 24 || bits != 32 {
		t.Errorf("Netmask = /%d (bits=%d), want /24 (bits=32)", ones, bits)
	}

	if cfg.Persist != false {
		t.Errorf("Persist = %v, want false", cfg.Persist)
	}
}

func TestDefaultConfig_AddressIsIPv4(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Address.To4() == nil {
		t.Error("Address should be IPv4")
	}
}

func TestDefaultConfig_NoIPv6(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Address6 != nil {
		t.Errorf("Address6 = %v, want nil", cfg.Address6)
	}
	if cfg.Netmask6 != nil {
		t.Errorf("Netmask6 = %v, want nil", cfg.Netmask6)
	}
}

func TestConfig_Fields(t *testing.T) {
	cfg := Config{
		Name:     "tun1",
		MTU:      1500,
		Address:  net.ParseIP("192.168.1.1"),
		Netmask:  net.CIDRMask(24, 32),
		Address6: net.ParseIP("fd00::1"),
		Netmask6: net.CIDRMask(64, 128),
		Persist:  true,
	}

	if cfg.Name != "tun1" {
		t.Errorf("Name = %s, want tun1", cfg.Name)
	}
	if cfg.MTU != 1500 {
		t.Errorf("MTU = %d, want 1500", cfg.MTU)
	}
	if !cfg.Address.Equal(net.ParseIP("192.168.1.1")) {
		t.Errorf("Address = %v, want 192.168.1.1", cfg.Address)
	}
	if !cfg.Address6.Equal(net.ParseIP("fd00::1")) {
		t.Errorf("Address6 = %v, want fd00::1", cfg.Address6)
	}
	if !cfg.Persist {
		t.Error("Persist = false, want true")
	}
}

func TestConfig_EmptyName(t *testing.T) {
	cfg := Config{
		Name: "",
		MTU:  1400,
	}

	if cfg.Name != "" {
		t.Errorf("Name = %s, want empty string", cfg.Name)
	}
}

func TestConfig_ZeroMTU(t *testing.T) {
	cfg := Config{
		Name: "tun0",
		MTU:  0,
	}

	if cfg.MTU != 0 {
		t.Errorf("MTU = %d, want 0", cfg.MTU)
	}
}

func TestConfig_LargeMTU(t *testing.T) {
	cfg := Config{
		Name: "tun0",
		MTU:  65535,
	}

	if cfg.MTU != 65535 {
		t.Errorf("MTU = %d, want 65535", cfg.MTU)
	}
}

func TestConfig_IPv6Only(t *testing.T) {
	cfg := Config{
		Name:     "tun0",
		MTU:      1400,
		Address6: net.ParseIP("2001:db8::1"),
		Netmask6: net.CIDRMask(64, 128),
	}

	if cfg.Address != nil {
		t.Errorf("Address = %v, want nil", cfg.Address)
	}
	if cfg.Address6 == nil {
		t.Error("Address6 should not be nil")
	}

	ones, bits := cfg.Netmask6.Size()
	if ones != 64 || bits != 128 {
		t.Errorf("Netmask6 = /%d (bits=%d), want /64 (bits=128)", ones, bits)
	}
}

func TestConfig_DualStack(t *testing.T) {
	cfg := Config{
		Name:     "tun0",
		MTU:      1400,
		Address:  net.ParseIP("10.0.0.1"),
		Netmask:  net.CIDRMask(24, 32),
		Address6: net.ParseIP("fd00::1"),
		Netmask6: net.CIDRMask(64, 128),
	}

	// Verify IPv4
	if cfg.Address.To4() == nil {
		t.Error("Address should be IPv4")
	}

	// Verify IPv6
	if cfg.Address6.To4() != nil {
		t.Error("Address6 should be IPv6")
	}
}

func TestNew_UnsupportedPlatform(t *testing.T) {
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		t.Skip("TUN is supported on this platform; covered by platform-specific tests")
	}

	cfg := DefaultConfig()
	dev, err := New(cfg)

	if err == nil {
		t.Error("New() should return error on unsupported platforms")
	}
	if dev != nil {
		t.Error("New() should return nil device on unsupported platforms")
	}
	if !strings.Contains(err.Error(), runtime.GOOS) {
		t.Errorf("error should mention current platform %s, got: %v", runtime.GOOS, err)
	}
}

// Interface compliance test (compile-time verification)
func TestDevice_InterfaceMethods(t *testing.T) {
	// Verify Device interface has expected methods
	// This is a compile-time check - if it compiles, the interface is correct
	var _ Device = (*mockDevice)(nil)
}

// mockDevice implements Device for testing interface compliance
type mockDevice struct {
	name string
	mtu  int
	fd   int
	data []byte
}

func (m *mockDevice) Read(p []byte) (int, error) {
	n := copy(p, m.data)
	return n, nil
}

func (m *mockDevice) Write(p []byte) (int, error) {
	m.data = make([]byte, len(p))
	copy(m.data, p)
	return len(p), nil
}

func (m *mockDevice) Close() error {
	return nil
}

func (m *mockDevice) Name() string {
	return m.name
}

func (m *mockDevice) MTU() int {
	return m.mtu
}

func (m *mockDevice) SetMTU(mtu int) error {
	m.mtu = mtu
	return nil
}

func (m *mockDevice) File() int {
	return m.fd
}

func TestMockDevice_Read(t *testing.T) {
	dev := &mockDevice{data: []byte("test data")}

	buf := make([]byte, 100)
	n, err := dev.Read(buf)

	if err != nil {
		t.Errorf("Read error = %v", err)
	}
	if n != 9 {
		t.Errorf("Read n = %d, want 9", n)
	}
	if string(buf[:n]) != "test data" {
		t.Errorf("Read data = %q, want 'test data'", buf[:n])
	}
}

func TestMockDevice_Write(t *testing.T) {
	dev := &mockDevice{}

	data := []byte("write test")
	n, err := dev.Write(data)

	if err != nil {
		t.Errorf("Write error = %v", err)
	}
	if n != len(data) {
		t.Errorf("Write n = %d, want %d", n, len(data))
	}
	if string(dev.data) != string(data) {
		t.Errorf("stored data = %q, want %q", dev.data, data)
	}
}

func TestMockDevice_Name(t *testing.T) {
	dev := &mockDevice{name: "tun0"}

	if dev.Name() != "tun0" {
		t.Errorf("Name() = %s, want tun0", dev.Name())
	}
}

func TestMockDevice_MTU(t *testing.T) {
	dev := &mockDevice{mtu: 1400}

	if dev.MTU() != 1400 {
		t.Errorf("MTU() = %d, want 1400", dev.MTU())
	}
}

func TestMockDevice_SetMTU(t *testing.T) {
	dev := &mockDevice{mtu: 1400}

	err := dev.SetMTU(1500)
	if err != nil {
		t.Errorf("SetMTU error = %v", err)
	}
	if dev.MTU() != 1500 {
		t.Errorf("MTU() after SetMTU = %d, want 1500", dev.MTU())
	}
}

func TestMockDevice_File(t *testing.T) {
	dev := &mockDevice{fd: 42}

	if dev.File() != 42 {
		t.Errorf("File() = %d, want 42", dev.File())
	}
}

func TestMockDevice_Close(t *testing.T) {
	dev := &mockDevice{}

	err := dev.Close()
	if err != nil {
		t.Errorf("Close error = %v", err)
	}
}

// Benchmark tests

func BenchmarkDefaultConfig(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = DefaultConfig()
	}
}

func BenchmarkMockDevice_ReadWrite(b *testing.B) {
	dev := &mockDevice{}
	data := make([]byte, 1400)
	buf := make([]byte, 1400)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dev.Write(data)
		dev.Read(buf)
	}
}
