package stack

import (
	"context"
	"net"
	"testing"

	"go.uber.org/zap"
	"gvisor.dev/gvisor/pkg/tcpip/header"
)

// --- BuildUDPResponse Tests ---

func TestEffectiveMaxTCPInFlight(t *testing.T) {
	if got := (Config{}).effectiveMaxTCPInFlight(); got != defaultMaxTCPInFlight {
		t.Errorf("default effectiveMaxTCPInFlight = %d, want %d", got, defaultMaxTCPInFlight)
	}
	if got := (Config{MaxTCPInFlight: 64}).effectiveMaxTCPInFlight(); got != 64 {
		t.Errorf("effectiveMaxTCPInFlight = %d, want 64", got)
	}
	// Negative values fall back to the default.
	if got := (Config{MaxTCPInFlight: -1}).effectiveMaxTCPInFlight(); got != defaultMaxTCPInFlight {
		t.Errorf("negative effectiveMaxTCPInFlight = %d, want %d", got, defaultMaxTCPInFlight)
	}
	// The cap must stay bounded: 65535 allowed a SYN scan to amplify into
	// tens of thousands of concurrent mesh connections.
	if defaultMaxTCPInFlight > 4096 {
		t.Errorf("defaultMaxTCPInFlight = %d, want a bounded value <= 4096", defaultMaxTCPInFlight)
	}
}

func TestBuildUDPResponse_Basic(t *testing.T) {
	srcIP := net.ParseIP("192.168.1.1").To4()
	dstIP := net.ParseIP("10.0.0.1").To4()
	srcPort := uint16(12345)
	dstPort := uint16(53)
	payload := []byte("test payload")

	pkt := BuildUDPResponse(srcIP, dstIP, srcPort, dstPort, payload)

	// Verify packet length
	expectedLen := header.IPv4MinimumSize + header.UDPMinimumSize + len(payload)
	if len(pkt) != expectedLen {
		t.Errorf("packet length = %d, want %d", len(pkt), expectedLen)
	}

	// Parse and verify IP header
	ipHdr := header.IPv4(pkt)

	// Check version via first nibble
	version := pkt[0] >> 4
	if version != 4 {
		t.Errorf("IP version = %d, want 4", version)
	}

	if ipHdr.HeaderLength() != header.IPv4MinimumSize {
		t.Errorf("IP header length = %d, want %d", ipHdr.HeaderLength(), header.IPv4MinimumSize)
	}

	if ipHdr.TotalLength() != uint16(expectedLen) {
		t.Errorf("IP total length = %d, want %d", ipHdr.TotalLength(), expectedLen)
	}

	if ipHdr.TTL() != 64 {
		t.Errorf("TTL = %d, want 64", ipHdr.TTL())
	}

	if ipHdr.Protocol() != uint8(header.UDPProtocolNumber) {
		t.Errorf("protocol = %d, want %d (UDP)", ipHdr.Protocol(), header.UDPProtocolNumber)
	}

	srcAddr := ipHdr.SourceAddress()
	gotSrcIP := net.IP(srcAddr.AsSlice())
	if !gotSrcIP.Equal(srcIP) {
		t.Errorf("source IP = %v, want %v", gotSrcIP, srcIP)
	}

	dstAddr := ipHdr.DestinationAddress()
	gotDstIP := net.IP(dstAddr.AsSlice())
	if !gotDstIP.Equal(dstIP) {
		t.Errorf("destination IP = %v, want %v", gotDstIP, dstIP)
	}

	// Verify IP checksum is valid
	if ipHdr.CalculateChecksum() != 0xffff {
		t.Error("IP checksum is invalid")
	}
}

func TestBuildUDPResponse_UDPHeader(t *testing.T) {
	srcIP := net.ParseIP("10.10.10.10").To4()
	dstIP := net.ParseIP("20.20.20.20").To4()
	srcPort := uint16(8080)
	dstPort := uint16(443)
	payload := []byte("hello world")

	pkt := BuildUDPResponse(srcIP, dstIP, srcPort, dstPort, payload)

	// Parse UDP header
	udpHdr := header.UDP(pkt[header.IPv4MinimumSize:])

	if udpHdr.SourcePort() != srcPort {
		t.Errorf("UDP source port = %d, want %d", udpHdr.SourcePort(), srcPort)
	}

	if udpHdr.DestinationPort() != dstPort {
		t.Errorf("UDP destination port = %d, want %d", udpHdr.DestinationPort(), dstPort)
	}

	expectedUDPLen := header.UDPMinimumSize + len(payload)
	if udpHdr.Length() != uint16(expectedUDPLen) {
		t.Errorf("UDP length = %d, want %d", udpHdr.Length(), expectedUDPLen)
	}

	// Verify payload
	gotPayload := pkt[header.IPv4MinimumSize+header.UDPMinimumSize:]
	if string(gotPayload) != string(payload) {
		t.Errorf("payload = %q, want %q", gotPayload, payload)
	}
}

func TestBuildUDPResponse_EmptyPayload(t *testing.T) {
	srcIP := net.ParseIP("1.2.3.4").To4()
	dstIP := net.ParseIP("5.6.7.8").To4()
	srcPort := uint16(1234)
	dstPort := uint16(5678)
	payload := []byte{}

	pkt := BuildUDPResponse(srcIP, dstIP, srcPort, dstPort, payload)

	expectedLen := header.IPv4MinimumSize + header.UDPMinimumSize
	if len(pkt) != expectedLen {
		t.Errorf("packet length = %d, want %d", len(pkt), expectedLen)
	}

	// Verify UDP length
	udpHdr := header.UDP(pkt[header.IPv4MinimumSize:])
	if udpHdr.Length() != header.UDPMinimumSize {
		t.Errorf("UDP length = %d, want %d", udpHdr.Length(), header.UDPMinimumSize)
	}
}

func TestBuildUDPResponse_LargePayload(t *testing.T) {
	srcIP := net.ParseIP("192.168.0.1").To4()
	dstIP := net.ParseIP("192.168.0.2").To4()
	srcPort := uint16(9999)
	dstPort := uint16(8888)

	// Create 1000 byte payload
	payload := make([]byte, 1000)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	pkt := BuildUDPResponse(srcIP, dstIP, srcPort, dstPort, payload)

	expectedLen := header.IPv4MinimumSize + header.UDPMinimumSize + len(payload)
	if len(pkt) != expectedLen {
		t.Errorf("packet length = %d, want %d", len(pkt), expectedLen)
	}

	// Verify payload integrity
	gotPayload := pkt[header.IPv4MinimumSize+header.UDPMinimumSize:]
	for i := range payload {
		if gotPayload[i] != payload[i] {
			t.Errorf("payload byte %d = %d, want %d", i, gotPayload[i], payload[i])
			break
		}
	}
}

func TestBuildUDPResponse_DifferentAddresses(t *testing.T) {
	tests := []struct {
		name    string
		srcIP   string
		dstIP   string
		srcPort uint16
		dstPort uint16
	}{
		{
			name:    "localhost addresses",
			srcIP:   "127.0.0.1",
			dstIP:   "127.0.0.2",
			srcPort: 1000,
			dstPort: 2000,
		},
		{
			name:    "private network",
			srcIP:   "10.0.0.1",
			dstIP:   "10.255.255.254",
			srcPort: 80,
			dstPort: 443,
		},
		{
			name:    "public addresses",
			srcIP:   "8.8.8.8",
			dstIP:   "1.1.1.1",
			srcPort: 53,
			dstPort: 53,
		},
		{
			name:    "edge ports",
			srcIP:   "192.168.1.1",
			dstIP:   "192.168.1.2",
			srcPort: 1,     // minimum
			dstPort: 65535, // maximum
		},
		{
			name:    "zero port",
			srcIP:   "10.0.0.1",
			dstIP:   "10.0.0.2",
			srcPort: 0,
			dstPort: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srcIP := net.ParseIP(tt.srcIP).To4()
			dstIP := net.ParseIP(tt.dstIP).To4()
			payload := []byte("test")

			pkt := BuildUDPResponse(srcIP, dstIP, tt.srcPort, tt.dstPort, payload)

			// Verify IP addresses
			ipHdr := header.IPv4(pkt)
			srcAddr := ipHdr.SourceAddress()
			dstAddr := ipHdr.DestinationAddress()
			gotSrcIP := net.IP(srcAddr.AsSlice())
			gotDstIP := net.IP(dstAddr.AsSlice())

			if !gotSrcIP.Equal(srcIP) {
				t.Errorf("source IP = %v, want %v", gotSrcIP, srcIP)
			}
			if !gotDstIP.Equal(dstIP) {
				t.Errorf("destination IP = %v, want %v", gotDstIP, dstIP)
			}

			// Verify ports
			udpHdr := header.UDP(pkt[header.IPv4MinimumSize:])
			if udpHdr.SourcePort() != tt.srcPort {
				t.Errorf("source port = %d, want %d", udpHdr.SourcePort(), tt.srcPort)
			}
			if udpHdr.DestinationPort() != tt.dstPort {
				t.Errorf("destination port = %d, want %d", udpHdr.DestinationPort(), tt.dstPort)
			}

			// Verify checksum is valid (non-zero unless zero checksum is valid)
			if ipHdr.CalculateChecksum() != 0xffff {
				t.Error("IP checksum is invalid")
			}
		})
	}
}

// --- checksum Tests ---

func TestChecksum_Empty(t *testing.T) {
	result := checksum([]byte{}, 0)
	if result != 0 {
		t.Errorf("checksum of empty data = %d, want 0", result)
	}
}

func TestChecksum_SingleByte(t *testing.T) {
	// Single byte (odd length) should be padded
	data := []byte{0x45}
	result := checksum(data, 0)

	// 0x45 << 8 = 0x4500
	expected := uint16(0x4500)
	if result != expected {
		t.Errorf("checksum = 0x%04x, want 0x%04x", result, expected)
	}
}

func TestChecksum_TwoBytes(t *testing.T) {
	data := []byte{0x12, 0x34}
	result := checksum(data, 0)

	expected := uint16(0x1234)
	if result != expected {
		t.Errorf("checksum = 0x%04x, want 0x%04x", result, expected)
	}
}

func TestChecksum_FourBytes(t *testing.T) {
	data := []byte{0x00, 0x01, 0x00, 0x02}
	result := checksum(data, 0)

	// 0x0001 + 0x0002 = 0x0003
	expected := uint16(0x0003)
	if result != expected {
		t.Errorf("checksum = 0x%04x, want 0x%04x", result, expected)
	}
}

func TestChecksum_WithOverflow(t *testing.T) {
	// Test carry-over behavior
	data := []byte{0xFF, 0xFF, 0x00, 0x01}
	result := checksum(data, 0)

	// 0xFFFF + 0x0001 = 0x10000 -> fold to 0x0001
	expected := uint16(0x0001)
	if result != expected {
		t.Errorf("checksum = 0x%04x, want 0x%04x", result, expected)
	}
}

func TestChecksum_WithInitial(t *testing.T) {
	data := []byte{0x00, 0x10}
	initial := uint16(0x0005)
	result := checksum(data, initial)

	// 0x0005 + 0x0010 = 0x0015
	expected := uint16(0x0015)
	if result != expected {
		t.Errorf("checksum = 0x%04x, want 0x%04x", result, expected)
	}
}

func TestChecksum_OddLength(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03}
	result := checksum(data, 0)

	// 0x0102 + 0x0300 = 0x0402
	expected := uint16(0x0402)
	if result != expected {
		t.Errorf("checksum = 0x%04x, want 0x%04x", result, expected)
	}
}

func TestChecksum_AllZeros(t *testing.T) {
	data := make([]byte, 100)
	result := checksum(data, 0)

	if result != 0 {
		t.Errorf("checksum of zeros = 0x%04x, want 0", result)
	}
}

func TestChecksum_AllOnes(t *testing.T) {
	data := []byte{0xFF, 0xFF, 0xFF, 0xFF}
	result := checksum(data, 0)

	// 0xFFFF + 0xFFFF = 0x1FFFE -> fold to 0xFFFF
	expected := uint16(0xFFFF)
	if result != expected {
		t.Errorf("checksum = 0x%04x, want 0x%04x", result, expected)
	}
}

func TestChecksum_LargeData(t *testing.T) {
	// Test with larger data to ensure no overflow issues
	data := make([]byte, 1000)
	for i := range data {
		data[i] = byte(i % 256)
	}

	// Should not panic
	result := checksum(data, 0)

	// Just verify it returns something reasonable (non-zero for this data)
	if result == 0 {
		t.Error("checksum of non-zero data should not be 0")
	}
}

// --- Config Tests ---

func TestConfig_DefaultMTU(t *testing.T) {
	cfg := Config{}
	if cfg.MTU != 0 {
		t.Errorf("default MTU = %d, want 0 (unset)", cfg.MTU)
	}
}

func TestConfig_FieldTypes(t *testing.T) {
	// Verify Config can hold expected values
	cfg := Config{
		MTU:      1500,
		IPv4Addr: net.ParseIP("10.0.0.1"),
		IPv4Mask: net.CIDRMask(24, 32),
		IPv6Addr: net.ParseIP("::1"),
		IPv6Mask: net.CIDRMask(64, 128),
	}

	if cfg.MTU != 1500 {
		t.Errorf("MTU = %d, want 1500", cfg.MTU)
	}

	if !cfg.IPv4Addr.Equal(net.ParseIP("10.0.0.1")) {
		t.Errorf("IPv4Addr = %v, want 10.0.0.1", cfg.IPv4Addr)
	}

	ones, bits := cfg.IPv4Mask.Size()
	if ones != 24 || bits != 32 {
		t.Errorf("IPv4Mask = /%d (bits=%d), want /24 (bits=32)", ones, bits)
	}
}

// --- Interface Tests (compile-time verification) ---

// Verify interfaces are correctly defined

type mockTCPHandler struct{}

func (m *mockTCPHandler) HandleTCP(ctx context.Context, conn net.Conn, srcAddr, dstAddr net.Addr) error {
	return nil
}

type mockTCPPreConnector struct{}

func (m *mockTCPPreConnector) HandleTCP(ctx context.Context, conn net.Conn, srcAddr, dstAddr net.Addr) error {
	return nil
}

func (m *mockTCPPreConnector) PreConnect(ctx context.Context, srcAddr, dstAddr net.Addr) (net.Conn, error) {
	return nil, nil
}

type mockRawUDPHandler struct{}

func (m *mockRawUDPHandler) HandleRawUDP(ctx context.Context, srcIP, dstIP net.IP, srcPort, dstPort uint16, payload []byte, reply UDPReplyFunc) error {
	return nil
}

type mockUDPPacketHandler struct{}

func (m *mockUDPPacketHandler) HandleUDPPacket(srcIP, dstIP net.IP, srcPort, dstPort uint16, payload []byte) {
}

func TestInterfaces_TCPHandler(t *testing.T) {
	var _ TCPHandler = &mockTCPHandler{}
}

func TestInterfaces_TCPPreConnector(t *testing.T) {
	var h TCPHandler = &mockTCPPreConnector{}
	_, ok := h.(TCPPreConnector)
	if !ok {
		t.Error("mockTCPPreConnector should implement TCPPreConnector")
	}
}

func TestInterfaces_RawUDPHandler(t *testing.T) {
	var _ RawUDPHandler = &mockRawUDPHandler{}
}

func TestInterfaces_UDPPacketHandler(t *testing.T) {
	var _ UDPPacketHandler = &mockUDPPacketHandler{}
}

// --- UDP Response Round-trip Test ---

func TestBuildUDPResponse_RoundTrip(t *testing.T) {
	// Build a packet and verify it can be fully parsed
	srcIP := net.ParseIP("172.16.0.1").To4()
	dstIP := net.ParseIP("172.16.0.2").To4()
	srcPort := uint16(5000)
	dstPort := uint16(6000)
	payload := []byte("DNS query simulation")

	pkt := BuildUDPResponse(srcIP, dstIP, srcPort, dstPort, payload)

	// Parse as IP packet
	ipHdr := header.IPv4(pkt)

	// Verify IP header fields - check version via first nibble
	version := pkt[0] >> 4
	if version != 4 {
		t.Fatalf("IP version = %d, want 4", version)
	}

	totalLen := ipHdr.TotalLength()
	if int(totalLen) != len(pkt) {
		t.Errorf("total length mismatch: header says %d, actual %d", totalLen, len(pkt))
	}

	// Parse UDP
	ipHdrLen := ipHdr.HeaderLength()
	udpHdr := header.UDP(pkt[ipHdrLen:])

	// Verify UDP fields match input
	if udpHdr.SourcePort() != srcPort {
		t.Errorf("source port = %d, want %d", udpHdr.SourcePort(), srcPort)
	}
	if udpHdr.DestinationPort() != dstPort {
		t.Errorf("dest port = %d, want %d", udpHdr.DestinationPort(), dstPort)
	}

	// Extract and verify payload
	udpPayload := pkt[int(ipHdrLen)+header.UDPMinimumSize:]
	if string(udpPayload) != string(payload) {
		t.Errorf("payload = %q, want %q", udpPayload, payload)
	}
}

// --- interceptEndpoint Tests ---

func TestNewInterceptEndpoint(t *testing.T) {
	var writeCallCount int
	tunWriter := func(data []byte) (int, error) {
		writeCallCount++
		return len(data), nil
	}

	mockHandler := &mockUDPPacketHandler{}
	logger := zap.NewNop()

	// Using nil for inner endpoint and ICMP handler since we're testing the wrapper
	ep := newInterceptEndpoint(nil, mockHandler, nil, tunWriter, logger)

	if ep == nil {
		t.Fatal("newInterceptEndpoint returned nil")
	}
	if ep.udpHandler != mockHandler {
		t.Error("udpHandler not set correctly")
	}
	if ep.tunWriter == nil {
		t.Error("tunWriter not set")
	}
	if ep.logger != logger {
		t.Error("logger not set correctly")
	}
}

// --- Config with Handlers Tests ---

func TestConfig_WithHandlers(t *testing.T) {
	tcpHandler := &mockTCPHandler{}
	rawUDPHandler := &mockRawUDPHandler{}

	cfg := Config{
		MTU:           1400,
		IPv4Addr:      net.ParseIP("10.200.200.1"),
		IPv4Mask:      net.CIDRMask(24, 32),
		TCPHandler:    tcpHandler,
		RawUDPHandler: rawUDPHandler,
		Logger:        zap.NewNop(),
	}

	if cfg.TCPHandler == nil {
		t.Error("TCPHandler should not be nil")
	}
	if cfg.RawUDPHandler == nil {
		t.Error("RawUDPHandler should not be nil")
	}
	if cfg.Logger == nil {
		t.Error("Logger should not be nil")
	}
}

func TestConfig_IPv6(t *testing.T) {
	cfg := Config{
		IPv6Addr: net.ParseIP("fd00::1"),
		IPv6Mask: net.CIDRMask(64, 128),
	}

	if cfg.IPv6Addr == nil {
		t.Error("IPv6Addr should not be nil")
	}

	ones, bits := cfg.IPv6Mask.Size()
	if ones != 64 || bits != 128 {
		t.Errorf("IPv6Mask = /%d (bits=%d), want /64 (bits=128)", ones, bits)
	}
}

// --- checksum edge cases ---

func TestChecksum_MaxData(t *testing.T) {
	// Test with data that produces maximum checksum value
	data := []byte{0xFF, 0xFF}
	result := checksum(data, 0)

	if result != 0xFFFF {
		t.Errorf("checksum = 0x%04x, want 0xFFFF", result)
	}
}

func TestChecksum_MultipleOverflows(t *testing.T) {
	// Test with data that causes multiple carry-overs
	data := make([]byte, 10)
	for i := range data {
		data[i] = 0xFF
	}

	// Should not panic and should return valid result
	result := checksum(data, 0)

	// Result should be 0xFFFF due to folding
	if result != 0xFFFF {
		t.Errorf("checksum = 0x%04x, want 0xFFFF", result)
	}
}

func TestChecksum_InitialMaxValue(t *testing.T) {
	data := []byte{0x00, 0x01}
	initial := uint16(0xFFFF)
	result := checksum(data, initial)

	// 0xFFFF + 0x0001 = 0x10000 -> fold to 0x0001
	expected := uint16(0x0001)
	if result != expected {
		t.Errorf("checksum = 0x%04x, want 0x%04x", result, expected)
	}
}

// --- BuildUDPResponse edge cases ---

func TestBuildUDPResponse_MaxPayload(t *testing.T) {
	srcIP := net.ParseIP("10.0.0.1").To4()
	dstIP := net.ParseIP("10.0.0.2").To4()

	// UDP max payload is 65535 - 8 (UDP header) = 65527
	// But with IP header (20 bytes), practical max is smaller
	// Test with a reasonable large payload
	payload := make([]byte, 8192)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	pkt := BuildUDPResponse(srcIP, dstIP, 1234, 5678, payload)

	// Verify packet was created correctly
	expectedLen := header.IPv4MinimumSize + header.UDPMinimumSize + len(payload)
	if len(pkt) != expectedLen {
		t.Errorf("packet length = %d, want %d", len(pkt), expectedLen)
	}

	// Verify IP total length
	ipHdr := header.IPv4(pkt)
	if ipHdr.TotalLength() != uint16(expectedLen) {
		t.Errorf("IP total length = %d, want %d", ipHdr.TotalLength(), expectedLen)
	}
}

func TestBuildUDPResponse_BinaryPayload(t *testing.T) {
	srcIP := net.ParseIP("1.1.1.1").To4()
	dstIP := net.ParseIP("2.2.2.2").To4()

	// Create payload with all byte values
	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = byte(i)
	}

	pkt := BuildUDPResponse(srcIP, dstIP, 53, 53, payload)

	// Verify payload integrity
	gotPayload := pkt[header.IPv4MinimumSize+header.UDPMinimumSize:]
	for i := range payload {
		if gotPayload[i] != payload[i] {
			t.Errorf("payload[%d] = %d, want %d", i, gotPayload[i], payload[i])
			break
		}
	}
}

// --- Stack interface verification ---

func TestStack_ImplementsClose(t *testing.T) {
	// Verify Stack has Close method
	s := &Stack{}
	err := s.Close()
	// Should not panic, may return nil
	_ = err
}

// --- Benchmark Tests ---

func BenchmarkBuildUDPResponse_SmallPayload(b *testing.B) {
	srcIP := net.ParseIP("10.0.0.1").To4()
	dstIP := net.ParseIP("10.0.0.2").To4()
	payload := []byte("small")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildUDPResponse(srcIP, dstIP, 1234, 5678, payload)
	}
}

func BenchmarkBuildUDPResponse_MediumPayload(b *testing.B) {
	srcIP := net.ParseIP("10.0.0.1").To4()
	dstIP := net.ParseIP("10.0.0.2").To4()
	payload := make([]byte, 512)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildUDPResponse(srcIP, dstIP, 1234, 5678, payload)
	}
}

func BenchmarkBuildUDPResponse_LargePayload(b *testing.B) {
	srcIP := net.ParseIP("10.0.0.1").To4()
	dstIP := net.ParseIP("10.0.0.2").To4()
	payload := make([]byte, 4096)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildUDPResponse(srcIP, dstIP, 1234, 5678, payload)
	}
}

func BenchmarkChecksum_Small(b *testing.B) {
	data := make([]byte, 20)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = checksum(data, 0)
	}
}

func BenchmarkChecksum_Large(b *testing.B) {
	data := make([]byte, 1500)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = checksum(data, 0)
	}
}

func BenchmarkNewInterceptEndpoint(b *testing.B) {
	tunWriter := func(data []byte) (int, error) { return len(data), nil }
	handler := &mockUDPPacketHandler{}
	logger := zap.NewNop()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = newInterceptEndpoint(nil, handler, nil, tunWriter, logger)
	}
}

// --- ICMPv6 Tests ---

func TestBuildICMPEchoReply_IPv6(t *testing.T) {
	srcIP := net.ParseIP("2001:db8::1")
	dstIP := net.ParseIP("2001:db8::2")
	id := uint16(1234)
	seq := uint16(5678)
	payload := []byte("hello ipv6")

	pkt := BuildICMPEchoReply(srcIP, dstIP, id, seq, payload)

	// IPv6 header is 40 bytes, ICMPv6 echo is 8 bytes minimum
	expectedLen := 40 + 8 + len(payload)
	if len(pkt) != expectedLen {
		t.Errorf("packet length = %d, want %d", len(pkt), expectedLen)
	}

	// Check IPv6 header fields
	if pkt[0]>>4 != 6 {
		t.Errorf("IP version = %d, want 6", pkt[0]>>4)
	}

	// Next header should be ICMPv6 (58)
	if pkt[6] != 58 {
		t.Errorf("next header = %d, want 58 (ICMPv6)", pkt[6])
	}

	// Hop limit should be 64
	if pkt[7] != 64 {
		t.Errorf("hop limit = %d, want 64", pkt[7])
	}

	// Check ICMPv6 type (Echo Reply = 129)
	icmpOffset := 40
	if pkt[icmpOffset] != 129 {
		t.Errorf("ICMPv6 type = %d, want 129 (Echo Reply)", pkt[icmpOffset])
	}

	// Check ICMPv6 code (should be 0)
	if pkt[icmpOffset+1] != 0 {
		t.Errorf("ICMPv6 code = %d, want 0", pkt[icmpOffset+1])
	}

	// Check identifier
	gotID := uint16(pkt[icmpOffset+4])<<8 | uint16(pkt[icmpOffset+5])
	if gotID != id {
		t.Errorf("identifier = %d, want %d", gotID, id)
	}

	// Check sequence
	gotSeq := uint16(pkt[icmpOffset+6])<<8 | uint16(pkt[icmpOffset+7])
	if gotSeq != seq {
		t.Errorf("sequence = %d, want %d", gotSeq, seq)
	}

	// Check payload
	gotPayload := pkt[icmpOffset+8:]
	if string(gotPayload) != string(payload) {
		t.Errorf("payload = %q, want %q", gotPayload, payload)
	}
}

func TestBuildICMPEchoReply_IPv6_EmptyPayload(t *testing.T) {
	srcIP := net.ParseIP("::1")
	dstIP := net.ParseIP("::2")

	pkt := BuildICMPEchoReply(srcIP, dstIP, 100, 1, []byte{})

	// IPv6 header (40) + ICMPv6 echo header (8)
	expectedLen := 40 + 8
	if len(pkt) != expectedLen {
		t.Errorf("packet length = %d, want %d", len(pkt), expectedLen)
	}
}

func TestBuildICMPEchoReply_IPv6_Checksum(t *testing.T) {
	srcIP := net.ParseIP("2001:db8::1")
	dstIP := net.ParseIP("2001:db8::2")

	pkt := BuildICMPEchoReply(srcIP, dstIP, 1, 1, []byte("test"))

	// Checksum bytes are at offset 42-43 (IPv6 header 40 + ICMPv6 checksum offset 2)
	checksum := uint16(pkt[42])<<8 | uint16(pkt[43])
	if checksum == 0 {
		t.Error("checksum should not be zero")
	}
}

func TestBuildICMPEchoReply_IPv6_LargePayload(t *testing.T) {
	srcIP := net.ParseIP("fe80::1")
	dstIP := net.ParseIP("fe80::2")
	payload := make([]byte, 1024)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	pkt := BuildICMPEchoReply(srcIP, dstIP, 9999, 100, payload)

	expectedLen := 40 + 8 + len(payload)
	if len(pkt) != expectedLen {
		t.Errorf("packet length = %d, want %d", len(pkt), expectedLen)
	}

	// Verify payload integrity
	gotPayload := pkt[48:]
	for i := 0; i < len(payload); i++ {
		if gotPayload[i] != payload[i] {
			t.Errorf("payload mismatch at index %d: got %d, want %d", i, gotPayload[i], payload[i])
			break
		}
	}
}

func BenchmarkBuildICMPEchoReply_IPv6(b *testing.B) {
	srcIP := net.ParseIP("2001:db8::1")
	dstIP := net.ParseIP("2001:db8::2")
	payload := []byte("benchmark payload data")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildICMPEchoReply(srcIP, dstIP, 1234, uint16(i), payload)
	}
}

// --- IPv6 UDP Tests ---

func TestBuildUDPResponse_IPv6(t *testing.T) {
	srcIP := net.ParseIP("2001:db8::1")
	dstIP := net.ParseIP("2001:db8::2")
	srcPort := uint16(12345)
	dstPort := uint16(53)
	payload := []byte("hello ipv6 udp")

	pkt := BuildUDPResponse(srcIP, dstIP, srcPort, dstPort, payload)

	// IPv6 header is 40 bytes, UDP header is 8 bytes
	expectedLen := 40 + 8 + len(payload)
	if len(pkt) != expectedLen {
		t.Errorf("packet length = %d, want %d", len(pkt), expectedLen)
	}

	// Check IPv6 header fields
	if pkt[0]>>4 != 6 {
		t.Errorf("IP version = %d, want 6", pkt[0]>>4)
	}

	// Next header should be UDP (17)
	if pkt[6] != 17 {
		t.Errorf("next header = %d, want 17 (UDP)", pkt[6])
	}

	// Hop limit should be 64
	if pkt[7] != 64 {
		t.Errorf("hop limit = %d, want 64", pkt[7])
	}

	// Check UDP header
	udpOffset := 40

	// Source port
	gotSrcPort := uint16(pkt[udpOffset])<<8 | uint16(pkt[udpOffset+1])
	if gotSrcPort != srcPort {
		t.Errorf("source port = %d, want %d", gotSrcPort, srcPort)
	}

	// Destination port
	gotDstPort := uint16(pkt[udpOffset+2])<<8 | uint16(pkt[udpOffset+3])
	if gotDstPort != dstPort {
		t.Errorf("destination port = %d, want %d", gotDstPort, dstPort)
	}

	// UDP length
	gotUDPLen := uint16(pkt[udpOffset+4])<<8 | uint16(pkt[udpOffset+5])
	expectedUDPLen := uint16(8 + len(payload))
	if gotUDPLen != expectedUDPLen {
		t.Errorf("UDP length = %d, want %d", gotUDPLen, expectedUDPLen)
	}

	// Check payload
	gotPayload := pkt[udpOffset+8:]
	if string(gotPayload) != string(payload) {
		t.Errorf("payload = %q, want %q", gotPayload, payload)
	}
}

func TestBuildUDPResponse_IPv6_EmptyPayload(t *testing.T) {
	srcIP := net.ParseIP("::1")
	dstIP := net.ParseIP("::2")

	pkt := BuildUDPResponse(srcIP, dstIP, 1234, 5678, []byte{})

	// IPv6 header (40) + UDP header (8)
	expectedLen := 40 + 8
	if len(pkt) != expectedLen {
		t.Errorf("packet length = %d, want %d", len(pkt), expectedLen)
	}
}

func TestBuildUDPResponse_IPv6_Checksum(t *testing.T) {
	srcIP := net.ParseIP("2001:db8::1")
	dstIP := net.ParseIP("2001:db8::2")

	pkt := BuildUDPResponse(srcIP, dstIP, 1234, 5678, []byte("test"))

	// Checksum bytes are at offset 46-47 (IPv6 header 40 + UDP checksum offset 6)
	checksum := uint16(pkt[46])<<8 | uint16(pkt[47])
	if checksum == 0 {
		t.Error("checksum should not be zero (mandatory for IPv6 UDP)")
	}
}

func TestBuildUDPResponse_IPv6_LargePayload(t *testing.T) {
	srcIP := net.ParseIP("fe80::1")
	dstIP := net.ParseIP("fe80::2")
	payload := make([]byte, 1024)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	pkt := BuildUDPResponse(srcIP, dstIP, 9999, 100, payload)

	expectedLen := 40 + 8 + len(payload)
	if len(pkt) != expectedLen {
		t.Errorf("packet length = %d, want %d", len(pkt), expectedLen)
	}

	// Verify payload integrity
	gotPayload := pkt[48:]
	for i := 0; i < len(payload); i++ {
		if gotPayload[i] != payload[i] {
			t.Errorf("payload mismatch at index %d: got %d, want %d", i, gotPayload[i], payload[i])
			break
		}
	}
}

func TestBuildUDPResponse_IPv6_LinkLocal(t *testing.T) {
	// Test with link-local addresses
	srcIP := net.ParseIP("fe80::1")
	dstIP := net.ParseIP("fe80::2")

	pkt := BuildUDPResponse(srcIP, dstIP, 546, 547, []byte("dhcpv6"))

	if len(pkt) != 40+8+6 {
		t.Errorf("packet length = %d, want %d", len(pkt), 40+8+6)
	}

	// Verify it's a valid IPv6 packet
	if pkt[0]>>4 != 6 {
		t.Errorf("IP version = %d, want 6", pkt[0]>>4)
	}
}

func BenchmarkBuildUDPResponse_IPv6(b *testing.B) {
	srcIP := net.ParseIP("2001:db8::1")
	dstIP := net.ParseIP("2001:db8::2")
	payload := []byte("benchmark payload data")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildUDPResponse(srcIP, dstIP, uint16(i), 53, payload)
	}
}

// --- IPv6 Integration Tests ---

// TestIPv6Detection verifies that IPv4 and IPv6 addresses are correctly detected
func TestIPv6Detection(t *testing.T) {
	tests := []struct {
		name   string
		ip     net.IP
		isIPv6 bool
	}{
		{"IPv4 dotted", net.ParseIP("192.168.1.1"), false},
		{"IPv4 loopback", net.ParseIP("127.0.0.1"), false},
		{"IPv4 broadcast", net.ParseIP("255.255.255.255"), false},
		{"IPv6 global", net.ParseIP("2001:db8::1"), true},
		{"IPv6 loopback", net.ParseIP("::1"), true},
		{"IPv6 link-local", net.ParseIP("fe80::1"), true},
		{"IPv6 full", net.ParseIP("2001:0db8:85a3:0000:0000:8a2e:0370:7334"), true},
		{"IPv4-mapped IPv6", net.ParseIP("::ffff:192.168.1.1"), false}, // To4() returns non-nil
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The detection logic used in stack.go: ip.To4() == nil means IPv6
			isIPv6 := tt.ip.To4() == nil
			if isIPv6 != tt.isIPv6 {
				t.Errorf("To4() == nil for %s = %v, want %v", tt.ip, isIPv6, tt.isIPv6)
			}
		})
	}
}

// TestIPv6UDPRoundTrip verifies that IPv6 UDP packets can be built and parsed back
func TestIPv6UDPRoundTrip(t *testing.T) {
	srcIP := net.ParseIP("2001:db8::1")
	dstIP := net.ParseIP("2001:db8::2")
	srcPort := uint16(12345)
	dstPort := uint16(53)
	payload := []byte("DNS query data")

	// Build the packet
	pkt := BuildUDPResponse(srcIP, dstIP, srcPort, dstPort, payload)

	// Parse IPv6 header using gVisor
	ipHdr := header.IPv6(pkt)

	// Verify version
	if ipHdr.PayloadLength() != uint16(header.UDPMinimumSize+len(payload)) {
		t.Errorf("payload length = %d, want %d", ipHdr.PayloadLength(), header.UDPMinimumSize+len(payload))
	}

	// Verify next header is UDP
	if ipHdr.TransportProtocol() != header.UDPProtocolNumber {
		t.Errorf("transport protocol = %d, want %d (UDP)", ipHdr.TransportProtocol(), header.UDPProtocolNumber)
	}

	// Extract and verify source/destination addresses
	srcAddr := ipHdr.SourceAddress()
	dstAddr := ipHdr.DestinationAddress()
	extractedSrc := net.IP(srcAddr.AsSlice())
	extractedDst := net.IP(dstAddr.AsSlice())

	if !extractedSrc.Equal(srcIP) {
		t.Errorf("source IP = %s, want %s", extractedSrc, srcIP)
	}
	if !extractedDst.Equal(dstIP) {
		t.Errorf("dest IP = %s, want %s", extractedDst, dstIP)
	}

	// Parse UDP header
	udpHdr := header.UDP(pkt[header.IPv6MinimumSize:])

	if udpHdr.SourcePort() != srcPort {
		t.Errorf("source port = %d, want %d", udpHdr.SourcePort(), srcPort)
	}
	if udpHdr.DestinationPort() != dstPort {
		t.Errorf("dest port = %d, want %d", udpHdr.DestinationPort(), dstPort)
	}

	// Verify payload
	extractedPayload := pkt[header.IPv6MinimumSize+header.UDPMinimumSize:]
	if string(extractedPayload) != string(payload) {
		t.Errorf("payload = %q, want %q", extractedPayload, payload)
	}
}

// TestIPv6ICMPRoundTrip verifies that ICMPv6 packets can be built and parsed back
func TestIPv6ICMPRoundTrip(t *testing.T) {
	srcIP := net.ParseIP("2001:db8::1")
	dstIP := net.ParseIP("2001:db8::2")
	id := uint16(1234)
	seq := uint16(5)
	payload := []byte("ping payload")

	// Build the packet
	pkt := BuildICMPEchoReply(srcIP, dstIP, id, seq, payload)

	// Parse IPv6 header using gVisor
	ipHdr := header.IPv6(pkt)

	// Verify next header is ICMPv6
	if ipHdr.TransportProtocol() != header.ICMPv6ProtocolNumber {
		t.Errorf("transport protocol = %d, want %d (ICMPv6)", ipHdr.TransportProtocol(), header.ICMPv6ProtocolNumber)
	}

	// Parse ICMPv6 header
	icmpHdr := header.ICMPv6(pkt[header.IPv6MinimumSize:])

	// Verify type is Echo Reply (129)
	if icmpHdr.Type() != header.ICMPv6EchoReply {
		t.Errorf("ICMP type = %d, want %d (Echo Reply)", icmpHdr.Type(), header.ICMPv6EchoReply)
	}

	// Verify code is 0
	if icmpHdr.Code() != 0 {
		t.Errorf("ICMP code = %d, want 0", icmpHdr.Code())
	}

	// Extract identifier and sequence from ICMPv6 echo header
	// Format: Type(1) + Code(1) + Checksum(2) + Identifier(2) + Sequence(2) + Payload
	icmpData := pkt[header.IPv6MinimumSize:]
	extractedID := uint16(icmpData[4])<<8 | uint16(icmpData[5])
	extractedSeq := uint16(icmpData[6])<<8 | uint16(icmpData[7])

	if extractedID != id {
		t.Errorf("ICMP id = %d, want %d", extractedID, id)
	}
	if extractedSeq != seq {
		t.Errorf("ICMP seq = %d, want %d", extractedSeq, seq)
	}

	// Verify payload
	extractedPayload := pkt[header.IPv6MinimumSize+8:] // ICMPv6 header is 8 bytes for echo
	if string(extractedPayload) != string(payload) {
		t.Errorf("payload = %q, want %q", extractedPayload, payload)
	}
}

// TestIPv6AddressTypes tests various IPv6 address types work correctly
func TestIPv6AddressTypes(t *testing.T) {
	tests := []struct {
		name    string
		srcIP   string
		dstIP   string
		payload string
	}{
		{"global unicast", "2001:db8::1", "2001:db8::2", "global"},
		{"link-local", "fe80::1", "fe80::2", "link-local"},
		{"loopback", "::1", "::1", "loopback"},
		{"unique local", "fd00::1", "fd00::2", "ula"},
		{"multicast to unicast", "2001:db8::1", "ff02::1", "multicast"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srcIP := net.ParseIP(tt.srcIP)
			dstIP := net.ParseIP(tt.dstIP)
			payload := []byte(tt.payload)

			// Test UDP
			udpPkt := BuildUDPResponse(srcIP, dstIP, 1234, 5678, payload)
			if udpPkt[0]>>4 != 6 {
				t.Errorf("UDP packet version = %d, want 6", udpPkt[0]>>4)
			}

			// Verify addresses in packet
			ipHdr := header.IPv6(udpPkt)
			pktSrcAddr := ipHdr.SourceAddress()
			pktDstAddr := ipHdr.DestinationAddress()
			if !net.IP(pktSrcAddr.AsSlice()).Equal(srcIP) {
				t.Errorf("UDP src = %s, want %s", pktSrcAddr, srcIP)
			}
			if !net.IP(pktDstAddr.AsSlice()).Equal(dstIP) {
				t.Errorf("UDP dst = %s, want %s", pktDstAddr, dstIP)
			}

			// Test ICMP
			icmpPkt := BuildICMPEchoReply(srcIP, dstIP, 1, 1, payload)
			if icmpPkt[0]>>4 != 6 {
				t.Errorf("ICMP packet version = %d, want 6", icmpPkt[0]>>4)
			}
		})
	}
}
