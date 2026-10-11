package daemon

import (
	"testing"

	"go.uber.org/zap"

	"github.com/Muti-Metroo/Mutiauk/internal/config"
	meshdns "github.com/Muti-Metroo/Mutiauk/internal/dns"
)

// fakeIntegration records every Apply / Revert call so tests can
// assert on whether the daemon's diff cache short-circuited.
type fakeIntegration struct {
	applies   []([]string)
	reverts   int
	applyErr  error
	revertErr error
}

func (f *fakeIntegration) Name() string { return "fake" }

func (f *fakeIntegration) Apply(suffixes []string) error {
	cp := make([]string, len(suffixes))
	copy(cp, suffixes)
	f.applies = append(f.applies, cp)
	return f.applyErr
}

func (f *fakeIntegration) Revert() error {
	f.reverts++
	return f.revertErr
}

var _ meshdns.Integration = (*fakeIntegration)(nil)

func newServerForDNSTest(t *testing.T) (*Server, *fakeIntegration) {
	t.Helper()
	fake := &fakeIntegration{}
	s := &Server{
		logger:         zap.NewNop(),
		dnsListenIP:    "10.200.200.1",
		dnsIntegration: fake,
	}
	s.cfg.Store(&config.Config{TUN: config.TUNConfig{Name: "tun0"}})
	return s, fake
}

func TestApplyDNSIntegration_NilIntegrationIsNoOp(t *testing.T) {
	s, _ := newServerForDNSTest(t)
	s.dnsIntegration = nil
	s.applyDNSIntegration([]string{"mesh"})
	if s.applied.listenIP != "" || s.applied.suffixes != nil {
		t.Errorf("expected no state change when dnsIntegration is nil, got %+v", s.applied)
	}
}

func TestApplyDNSIntegration_AppliesOnFirstCall(t *testing.T) {
	s, fake := newServerForDNSTest(t)
	s.applyDNSIntegration([]string{"mesh"})
	if len(fake.applies) != 1 {
		t.Fatalf("expected 1 Apply, got %d", len(fake.applies))
	}
	if s.applied.listenIP != "10.200.200.1" || len(s.applied.suffixes) != 1 || s.applied.suffixes[0] != "mesh" {
		t.Errorf("applied state mismatch: %+v", s.applied)
	}
}

func TestApplyDNSIntegration_DiffSkipsNoOp(t *testing.T) {
	s, fake := newServerForDNSTest(t)
	s.applyDNSIntegration([]string{"mesh", "alt"})
	s.applyDNSIntegration([]string{"mesh", "alt"})
	if len(fake.applies) != 1 {
		t.Errorf("expected 1 Apply (diff cache should skip second), got %d", len(fake.applies))
	}
}

func TestApplyDNSIntegration_ListenIPChangeForcesReapply(t *testing.T) {
	s, fake := newServerForDNSTest(t)
	s.applyDNSIntegration([]string{"mesh"})
	if len(fake.applies) != 1 {
		t.Fatalf("expected 1 Apply after first call, got %d", len(fake.applies))
	}
	// Same suffixes but a different listen IP must trigger a re-apply.
	s.dnsListenIP = "10.200.200.2"
	s.applyDNSIntegration([]string{"mesh"})
	if len(fake.applies) != 2 {
		t.Errorf("expected 2 Apply calls after listenIP change, got %d", len(fake.applies))
	}
}

func TestApplyDNSIntegration_EmptySuffixReverts(t *testing.T) {
	s, fake := newServerForDNSTest(t)
	s.applyDNSIntegration([]string{"mesh"})
	s.applyDNSIntegration(nil)
	if fake.reverts != 1 {
		t.Errorf("expected 1 Revert on empty suffix list, got %d", fake.reverts)
	}
	if s.applied.suffixes != nil || s.applied.listenIP != "" {
		t.Errorf("applied state should reset on revert: %+v", s.applied)
	}
}
