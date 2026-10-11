//go:build darwin

package dns

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// withTempResolverDir redirects etcResolverDir to a temp dir and stubs the
// cache flush for the duration of the test.
func withTempResolverDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	origDir, origFlush := etcResolverDir, flushDNSCacheFn
	etcResolverDir = dir
	flushDNSCacheFn = func(*zap.Logger) {}
	t.Cleanup(func() {
		etcResolverDir = origDir
		flushDNSCacheFn = origFlush
	})
	return dir
}

func TestEtcResolver_ApplyReconcileRevert(t *testing.T) {
	dir := withTempResolverDir(t)
	ig := newEtcResolverIntegration("10.200.200.1", zap.NewNop())

	if err := ig.Apply([]string{"mesh", "corp"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, tld := range []string{"mesh", "corp"} {
		data, err := os.ReadFile(filepath.Join(dir, tld))
		if err != nil {
			t.Fatalf("expected resolver file %s: %v", tld, err)
		}
		if !strings.Contains(string(data), "nameserver 10.200.200.1") || !strings.Contains(string(data), "port 53") {
			t.Errorf("resolver file %s missing expected directives:\n%s", tld, data)
		}
		if !isManagedResolver(data) {
			t.Errorf("resolver file %s missing management marker", tld)
		}
	}

	// Reconcile: dropping "corp" must remove its file.
	if err := ig.Apply([]string{"mesh"}); err != nil {
		t.Fatalf("Apply (reconcile): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "corp")); !os.IsNotExist(err) {
		t.Error("corp resolver file should have been removed on reconcile")
	}
	if _, err := os.Stat(filepath.Join(dir, "mesh")); err != nil {
		t.Error("mesh resolver file should still exist")
	}

	if !etcResolverPresent() {
		t.Error("etcResolverPresent = false while a managed file exists")
	}

	// Revert removes everything we manage.
	if err := ig.Revert(); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mesh")); !os.IsNotExist(err) {
		t.Error("mesh resolver file should be removed after Revert")
	}
	if etcResolverPresent() {
		t.Error("etcResolverPresent = true after Revert")
	}
}

func TestEtcResolver_RefusesUnmanagedFile(t *testing.T) {
	dir := withTempResolverDir(t)
	// A pre-existing resolver file we do NOT own.
	if err := os.WriteFile(filepath.Join(dir, "mesh"), []byte("nameserver 9.9.9.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ig := newEtcResolverIntegration("10.200.200.1", zap.NewNop())
	if err := ig.Apply([]string{"mesh"}); err == nil {
		t.Error("Apply should refuse to overwrite an unmanaged resolver file")
	}
}

func TestEtcResolver_PreservesUnmanagedOnRevert(t *testing.T) {
	dir := withTempResolverDir(t)
	unmanaged := filepath.Join(dir, "corp")
	if err := os.WriteFile(unmanaged, []byte("nameserver 9.9.9.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ig := newEtcResolverIntegration("10.200.200.1", zap.NewNop())
	if err := ig.Apply([]string{"mesh"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := ig.Revert(); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if _, err := os.Stat(unmanaged); err != nil {
		t.Error("Revert must not remove a resolver file we do not own")
	}
}

func TestEtcResolver_Name(t *testing.T) {
	if got := newEtcResolverIntegration("", zap.NewNop()).Name(); got != IntegrationEtcResolver {
		t.Errorf("Name = %q, want %q", got, IntegrationEtcResolver)
	}
}

// TestEtcResolver_RefusesPathTraversal guards the defense-in-depth check in
// writeResolverFile: even if a malicious suffix somehow reaches the
// integration layer, the resolved path must stay inside the resolver dir.
func TestEtcResolver_RefusesPathTraversal(t *testing.T) {
	dir := withTempResolverDir(t)
	ig := newEtcResolverIntegration("10.200.200.1", zap.NewNop())

	outside := filepath.Join(dir, "..", "escaped")
	for _, tld := range []string{"a/../../escaped", "sub/dir"} {
		if err := ig.Apply([]string{tld}); err == nil {
			t.Errorf("Apply(%q): expected refusal, got nil error", tld)
		}
	}
	if _, err := os.Stat(filepath.Clean(outside)); !os.IsNotExist(err) {
		t.Error("traversal attempt created a file outside the resolver dir")
	}
}

func TestEtcResolverHostState(t *testing.T) {
	withTempResolverDir(t)
	ig := newEtcResolverIntegration("10.200.200.1", zap.NewNop())
	if err := ig.Apply([]string{"mesh"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	state, err := EtcResolverHostState()
	if err != nil {
		t.Fatalf("EtcResolverHostState: %v", err)
	}
	if !strings.Contains(state, "mesh") || !strings.Contains(state, "nameserver 10.200.200.1") {
		t.Errorf("host state missing expected content:\n%s", state)
	}
}

func TestDetectIntegration_AutoSelectsEtcResolverOnDarwin(t *testing.T) {
	ig, err := DetectIntegration("auto", "utun4", "10.200.200.1", zap.NewNop())
	if err != nil {
		t.Fatalf("DetectIntegration(auto): %v", err)
	}
	if ig.Name() != IntegrationEtcResolver {
		t.Errorf("auto backend = %q, want %q", ig.Name(), IntegrationEtcResolver)
	}
}
