package daemon

import (
	"testing"

	"go.uber.org/zap"
)

// TestCleanupOnUninitializedServer guards the partial-init rollback path:
// Run calls cleanup when initialize fails, so cleanup must tolerate a
// Server whose network components were never created.
func TestCleanupOnUninitializedServer(t *testing.T) {
	s, err := New(testConfig(), "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	s.cleanup() // all components nil; must not panic
}

// TestRetryStartDNSWithoutServicesCtx verifies the DNS retry is a safe
// no-op before startServices has stored a context (or after shutdown).
func TestRetryStartDNSWithoutServicesCtx(t *testing.T) {
	s, err := New(testConfig(), "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	s.retryStartDNS()
	if dnsServer, _, _ := s.dnsState(); dnsServer != nil {
		t.Fatal("dns server started without a services context")
	}
}
