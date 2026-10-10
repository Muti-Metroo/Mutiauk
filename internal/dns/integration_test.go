package dns

import (
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestDetectIntegration_NoneAlwaysReturnsNoop(t *testing.T) {
	got, err := DetectIntegration("none", "tun0", "10.0.0.1", zap.NewNop())
	if err != nil {
		t.Fatalf("DetectIntegration(none): %v", err)
	}
	if got.Name() != IntegrationNone {
		t.Errorf("name=%q, want %q", got.Name(), IntegrationNone)
	}
}

func TestDetectIntegration_UnknownPreference(t *testing.T) {
	_, err := DetectIntegration("bogus", "tun0", "10.0.0.1", zap.NewNop())
	if err == nil {
		t.Fatal("expected error for unknown preference")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error %q should mention the bad value", err)
	}
}

func TestDetectIntegration_ExplicitResolvectlWhenUnavailable(t *testing.T) {
	if resolvectlAvailable() {
		t.Skip("host has resolvectl; this test exercises the unavailable path")
	}
	_, err := DetectIntegration(IntegrationResolvectl, "tun0", "10.0.0.1", zap.NewNop())
	if err == nil {
		t.Fatal("expected error when resolvectl is requested but unavailable")
	}
}

func TestDetectIntegration_ExplicitNMDnsmasqWhenUnavailable(t *testing.T) {
	if nmDnsmasqAvailable() {
		t.Skip("host has NM dnsmasq plugin active; this test exercises the unavailable path")
	}
	_, err := DetectIntegration(IntegrationNMDnsmasq, "tun0", "10.0.0.1", zap.NewNop())
	if err == nil {
		t.Fatal("expected error when nm-dnsmasq is requested but unavailable")
	}
}

func TestDetectIntegration_AutoEmptyEqualsAuto(t *testing.T) {
	a, err := DetectIntegration("", "tun0", "10.0.0.1", zap.NewNop())
	if err != nil {
		t.Fatalf("empty preference: %v", err)
	}
	b, err := DetectIntegration("auto", "tun0", "10.0.0.1", zap.NewNop())
	if err != nil {
		t.Fatalf("auto preference: %v", err)
	}
	if a.Name() != b.Name() {
		t.Errorf("empty (%q) and auto (%q) chose different backends", a.Name(), b.Name())
	}
}

func TestNoopIntegration_ApplyRevertNoOp(t *testing.T) {
	n := newNoopIntegration(zap.NewNop())
	if err := n.Apply([]string{"mesh"}); err != nil {
		t.Errorf("noop Apply returned error: %v", err)
	}
	if err := n.Revert(); err != nil {
		t.Errorf("noop Revert returned error: %v", err)
	}
	if n.Name() != IntegrationNone {
		t.Errorf("name=%q, want %q", n.Name(), IntegrationNone)
	}
}
