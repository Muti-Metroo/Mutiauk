package dns

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	mdns "github.com/miekg/dns"
	"go.uber.org/zap"
)

func newTestServer(t *testing.T, suffixes ...string) (*Server, string) {
	t.Helper()

	if len(suffixes) == 0 {
		suffixes = []string{"mesh"}
	}

	s, err := New(Config{
		TTL:    30,
		Logger: zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.SetState(suffixes, nil)

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen probe: %v", err)
	}
	addr := pc.LocalAddr().String()
	pc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if err := s.Listen(ctx, addr); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	return s, addr
}

func setHosts(s *Server, hosts []Host) {
	s.SetState(s.Suffixes(), hosts)
}

func query(t *testing.T, addr, name string, qtype uint16) *mdns.Msg {
	t.Helper()
	c := &mdns.Client{Net: "udp", Timeout: 2 * time.Second}
	msg := new(mdns.Msg)
	msg.SetQuestion(mdns.Fqdn(name), qtype)
	resp, _, err := c.Exchange(msg, addr)
	if err != nil {
		t.Fatalf("dns exchange: %v", err)
	}
	return resp
}

func TestServer_AnswersA(t *testing.T) {
	s, addr := newTestServer(t)
	setHosts(s, []Host{
		{Name: "target-11.mesh", IPv4: net.ParseIP("10.8.0.11")},
	})

	resp := query(t, addr, "target-11.mesh", mdns.TypeA)
	if resp.Rcode != mdns.RcodeSuccess {
		t.Fatalf("rcode=%d, want NOERROR", resp.Rcode)
	}
	if !resp.Authoritative {
		t.Errorf("expected authoritative response")
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("answer count=%d, want 1", len(resp.Answer))
	}
	a, ok := resp.Answer[0].(*mdns.A)
	if !ok {
		t.Fatalf("answer is %T, want *dns.A", resp.Answer[0])
	}
	if !a.A.Equal(net.ParseIP("10.8.0.11")) {
		t.Errorf("answer IP=%v, want 10.8.0.11", a.A)
	}
	if a.Hdr.Ttl != 30 {
		t.Errorf("ttl=%d, want 30", a.Hdr.Ttl)
	}
}

func TestServer_AnswersAAAA(t *testing.T) {
	s, addr := newTestServer(t)
	setHosts(s, []Host{
		{Name: "v6.mesh", IPv6: net.ParseIP("fd00::11")},
	})

	resp := query(t, addr, "v6.mesh", mdns.TypeAAAA)
	if resp.Rcode != mdns.RcodeSuccess {
		t.Fatalf("rcode=%d, want NOERROR", resp.Rcode)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("answer count=%d, want 1", len(resp.Answer))
	}
}

func TestServer_NXDOMAIN_UnknownMeshName(t *testing.T) {
	_, addr := newTestServer(t)
	resp := query(t, addr, "missing.mesh", mdns.TypeA)
	if resp.Rcode != mdns.RcodeNameError {
		t.Fatalf("rcode=%d, want NXDOMAIN", resp.Rcode)
	}
	if len(resp.Answer) != 0 {
		t.Errorf("answer count=%d, want 0", len(resp.Answer))
	}
}

func TestServer_NXDOMAIN_OutsideSuffix(t *testing.T) {
	_, addr := newTestServer(t)
	resp := query(t, addr, "google.com", mdns.TypeA)
	if resp.Rcode != mdns.RcodeNameError {
		t.Fatalf("rcode=%d, want NXDOMAIN", resp.Rcode)
	}
}

func TestServer_NoAnswerForOtherType(t *testing.T) {
	s, addr := newTestServer(t)
	setHosts(s, []Host{
		{Name: "target-11.mesh", IPv4: net.ParseIP("10.8.0.11")},
	})

	resp := query(t, addr, "target-11.mesh", mdns.TypeMX)
	if resp.Rcode != mdns.RcodeSuccess {
		t.Fatalf("rcode=%d, want NOERROR", resp.Rcode)
	}
	if len(resp.Answer) != 0 {
		t.Errorf("expected no answer for MX, got %d", len(resp.Answer))
	}
}

func TestServer_AAAAWithOnlyIPv4_NoAnswer(t *testing.T) {
	s, addr := newTestServer(t)
	setHosts(s, []Host{
		{Name: "target-11.mesh", IPv4: net.ParseIP("10.8.0.11")},
	})

	resp := query(t, addr, "target-11.mesh", mdns.TypeAAAA)
	if resp.Rcode != mdns.RcodeSuccess {
		t.Fatalf("rcode=%d, want NOERROR (no error, just empty)", resp.Rcode)
	}
	if len(resp.Answer) != 0 {
		t.Errorf("expected empty AAAA, got %d", len(resp.Answer))
	}
}

func TestServer_SetStateDropsOutsideSuffix(t *testing.T) {
	s, _ := newTestServer(t)
	setHosts(s, []Host{
		{Name: "good.mesh", IPv4: net.ParseIP("10.8.0.1")},
		{Name: "bad.example", IPv4: net.ParseIP("10.8.0.2")},
		{Name: "", IPv4: net.ParseIP("10.8.0.3")},
	})
	if got := s.HostCount(); got != 1 {
		t.Errorf("HostCount=%d, want 1", got)
	}
}

func TestServer_SetStateCaseInsensitive(t *testing.T) {
	s, addr := newTestServer(t)
	setHosts(s, []Host{
		{Name: "Target-11.MESH", IPv4: net.ParseIP("10.8.0.11")},
	})
	resp := query(t, addr, "TARGET-11.mesh", mdns.TypeA)
	if resp.Rcode != mdns.RcodeSuccess || len(resp.Answer) != 1 {
		t.Fatalf("expected case-insensitive match, got rcode=%d answers=%d",
			resp.Rcode, len(resp.Answer))
	}
}

func TestServer_Lookup(t *testing.T) {
	s, err := New(Config{Logger: zap.NewNop()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.SetState([]string{"mesh"}, []Host{
		{Name: "Target-11.MESH", IPv4: net.ParseIP("10.8.0.11")},
	})

	got, ok := s.Lookup("target-11.mesh")
	if !ok {
		t.Fatalf("Lookup miss for registered name")
	}
	if !got.IPv4.Equal(net.ParseIP("10.8.0.11")) {
		t.Errorf("got IPv4 %v, want 10.8.0.11", got.IPv4)
	}

	if _, ok := s.Lookup("TARGET-11.mesh."); !ok {
		t.Errorf("Lookup should tolerate uppercase + trailing dot")
	}
	if _, ok := s.Lookup("missing.mesh"); ok {
		t.Errorf("Lookup should miss for unregistered name")
	}
}

func TestServer_HotSwap(t *testing.T) {
	s, addr := newTestServer(t)
	setHosts(s, []Host{{Name: "a.mesh", IPv4: net.ParseIP("10.0.0.1")}})

	resp := query(t, addr, "a.mesh", mdns.TypeA)
	if resp.Rcode != mdns.RcodeSuccess {
		t.Fatalf("first query rcode=%d", resp.Rcode)
	}

	setHosts(s, []Host{{Name: "b.mesh", IPv4: net.ParseIP("10.0.0.2")}})

	resp = query(t, addr, "a.mesh", mdns.TypeA)
	if resp.Rcode != mdns.RcodeNameError {
		t.Errorf("after swap, a.mesh rcode=%d, want NXDOMAIN", resp.Rcode)
	}
	resp = query(t, addr, "b.mesh", mdns.TypeA)
	if resp.Rcode != mdns.RcodeSuccess || len(resp.Answer) != 1 {
		t.Errorf("after swap, b.mesh rcode=%d answers=%d", resp.Rcode, len(resp.Answer))
	}
}

func TestServer_MultipleSuffixes(t *testing.T) {
	s, addr := newTestServer(t, "mesh", "alt")
	s.SetState([]string{"mesh", "alt"}, []Host{
		{Name: "a.mesh", IPv4: net.ParseIP("10.0.0.1")},
		{Name: "b.alt", IPv4: net.ParseIP("10.0.0.2")},
	})

	for _, name := range []string{"a.mesh", "b.alt"} {
		resp := query(t, addr, name, mdns.TypeA)
		if resp.Rcode != mdns.RcodeSuccess || len(resp.Answer) != 1 {
			t.Errorf("%s: rcode=%d answers=%d", name, resp.Rcode, len(resp.Answer))
		}
	}

	resp := query(t, addr, "c.other", mdns.TypeA)
	if resp.Rcode != mdns.RcodeNameError {
		t.Errorf("c.other: rcode=%d, want NXDOMAIN", resp.Rcode)
	}
}

func TestServer_MultiLabelSuffix(t *testing.T) {
	s, addr := newTestServer(t, "muti-metroo.lab")
	s.SetState([]string{"muti-metroo.lab"}, []Host{
		{Name: "host.muti-metroo.lab", IPv4: net.ParseIP("10.0.0.1")},
	})

	resp := query(t, addr, "host.muti-metroo.lab", mdns.TypeA)
	if resp.Rcode != mdns.RcodeSuccess || len(resp.Answer) != 1 {
		t.Fatalf("rcode=%d answers=%d", resp.Rcode, len(resp.Answer))
	}

	// "host.lab" must NOT match suffix "muti-metroo.lab"
	resp = query(t, addr, "host.lab", mdns.TypeA)
	if resp.Rcode != mdns.RcodeNameError {
		t.Errorf("host.lab: rcode=%d, want NXDOMAIN", resp.Rcode)
	}
}

func TestServer_SuffixShrinkDropsHosts(t *testing.T) {
	s, addr := newTestServer(t, "mesh", "alt")
	s.SetState([]string{"mesh", "alt"}, []Host{
		{Name: "a.mesh", IPv4: net.ParseIP("10.0.0.1")},
		{Name: "b.alt", IPv4: net.ParseIP("10.0.0.2")},
	})

	// Drop "alt" from active suffixes; b.alt must disappear and the
	// query must NXDOMAIN (no longer in any active suffix).
	s.SetState([]string{"mesh"}, []Host{
		{Name: "a.mesh", IPv4: net.ParseIP("10.0.0.1")},
	})

	resp := query(t, addr, "a.mesh", mdns.TypeA)
	if resp.Rcode != mdns.RcodeSuccess {
		t.Errorf("a.mesh: rcode=%d, want NOERROR", resp.Rcode)
	}
	resp = query(t, addr, "b.alt", mdns.TypeA)
	if resp.Rcode != mdns.RcodeNameError {
		t.Errorf("b.alt: rcode=%d, want NXDOMAIN", resp.Rcode)
	}
}

func TestServer_EmptySuffixesAlwaysNXDOMAIN(t *testing.T) {
	s, err := New(Config{Logger: zap.NewNop()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen probe: %v", err)
	}
	addr := pc.LocalAddr().String()
	pc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := s.Listen(ctx, addr); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	// No suffixes set -> every query is NXDOMAIN.
	for _, name := range []string{"target.mesh", "anything.com", "x"} {
		resp := query(t, addr, name, mdns.TypeA)
		if resp.Rcode != mdns.RcodeNameError {
			t.Errorf("%s: rcode=%d, want NXDOMAIN", name, resp.Rcode)
		}
	}
}

func TestServer_NormalizeSuffixesDedupsAndSorts(t *testing.T) {
	s, _ := newTestServer(t, "mesh") // initial single suffix
	got := s.SetState([]string{"alt", "MESH", " mesh ", ".mesh.", "alt", ""}, nil)
	want := []string{"alt", "mesh"}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d (got %v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}

// TestServer_NormalizeSuffixesRejectsInvalid is the security guard for
// agent-supplied suffixes: they are interpolated into dnsmasq drop-in
// lines and become /etc/resolver file names, so anything outside the
// strict DNS charset (config-line injection, path traversal) must be
// dropped, while valid single- and multi-label suffixes pass.
func TestServer_NormalizeSuffixesRejectsInvalid(t *testing.T) {
	s, err := New(Config{Logger: zap.NewNop()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	malicious := []string{
		"mesh\nserver=/#/6.6.6.6", // dnsmasq config-line injection
		"mesh\r\nserver=/#/6.6.6.6",
		"a/../../b",  // /etc/resolver path traversal
		"../etc",     // path traversal
		"a/b",        // path separator
		"ev il",      // interior space
		"semi;colon", // shell/dnsmasq metachar
		"quote\"x",
		"back\\slash",
		"-leading-hyphen",
		"trailing-hyphen-",
		"double..dot",
		"under_score",
		strings.Repeat("a", 254),           // over 253 chars total
		strings.Repeat("a", 64) + ".mesh",  // label over 63 chars
	}
	for _, suf := range malicious {
		if got := s.SetState([]string{suf}, nil); len(got) != 0 {
			t.Errorf("suffix %q: expected rejection, got %v", suf, got)
		}
	}

	valid := []string{"mesh", "muti-metroo.alt", "a.b.c", "x1", "0-9-0"}
	got := s.SetState(valid, nil)
	if len(got) != len(valid) {
		t.Errorf("valid suffixes dropped: got %v, want all of %v", got, valid)
	}

	// A mix keeps the valid entries and drops the malicious ones.
	got = s.SetState([]string{"mesh", "a/../../b", "alt"}, nil)
	if len(got) != 2 || got[0] != "alt" || got[1] != "mesh" {
		t.Errorf("mixed input: got %v, want [alt mesh]", got)
	}
}

func TestMatchesAnySuffix(t *testing.T) {
	tests := []struct {
		name     string
		suffixes []string
		want     bool
	}{
		{"target.mesh", []string{"mesh"}, true},
		{"target.mesh.", []string{"mesh"}, true}, // trailing dot
		{"TARGET.MESH", []string{"mesh"}, true},  // case-insensitive
		{"mesh", []string{"mesh"}, true},         // bare suffix
		{"target.alt", []string{"mesh"}, false},
		{"target.alt", []string{"mesh", "alt"}, true},
		{"host.muti-metroo.lab", []string{"muti-metroo.lab"}, true},
		{"host.lab", []string{"muti-metroo.lab"}, false},
		{"target.mesh", nil, false},
		{"target.mesh", []string{}, false},
		{"target.mesh", []string{""}, false},
		{"", []string{"mesh"}, false},
	}
	for _, tt := range tests {
		got := MatchesAnySuffix(tt.name, tt.suffixes)
		if got != tt.want {
			t.Errorf("MatchesAnySuffix(%q, %v) = %v, want %v", tt.name, tt.suffixes, got, tt.want)
		}
	}
}

// TestListenContextCancelReleasesAddresses is the regression guard for a
// listener leak: Listen's ctx.Done branches used to return without shutting
// down the UDP/TCP servers that had already started, so a cancelled startup
// left port 53 held by orphaned listeners.
func TestListenContextCancelReleasesAddresses(t *testing.T) {
	s, err := New(Config{Logger: zap.NewNop()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Grab a free port, then release it for the DNS server.
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	addr := probe.LocalAddr().String()
	probe.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before Listen starts anything

	if err := s.Listen(ctx, addr); err == nil {
		t.Fatal("Listen with pre-cancelled ctx returned nil error")
	}

	// Give any orphaned listener goroutine time to bind (with the leak,
	// the servers bind shortly after Listen returns). Afterwards both
	// protocols must be bindable: nothing may be left running.
	time.Sleep(200 * time.Millisecond)

	udpConn, udpErr := net.ListenPacket("udp", addr)
	if udpErr == nil {
		udpConn.Close()
	}
	tcpLn, tcpErr := net.Listen("tcp", addr)
	if tcpErr == nil {
		tcpLn.Close()
	}
	if udpErr != nil || tcpErr != nil {
		t.Fatalf("addresses still held after cancelled Listen: udp=%v tcp=%v", udpErr, tcpErr)
	}
}
