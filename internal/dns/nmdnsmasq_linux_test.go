//go:build linux

package dns

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"
)

// withTempDropInDir redirects nmDnsmasqDropInDir to a per-test temp
// directory and stubs the NetworkManager reload to a counter the test
// can assert on.
func withTempDropInDir(t *testing.T) (dir string, reloads *atomic.Int32) {
	t.Helper()

	prevDir := nmDnsmasqDropInDir
	prevFn := reloadNetworkManagerFn
	dir = t.TempDir()
	nmDnsmasqDropInDir = dir
	var ctr atomic.Int32
	reloadNetworkManagerFn = func() error {
		ctr.Add(1)
		return nil
	}
	t.Cleanup(func() {
		nmDnsmasqDropInDir = prevDir
		reloadNetworkManagerFn = prevFn
	})
	return dir, &ctr
}

func TestNMDnsmasq_ApplyWritesExpectedContents(t *testing.T) {
	dir, reloads := withTempDropInDir(t)

	n := newNMDnsmasqIntegration("10.200.200.1", zap.NewNop())
	if err := n.Apply([]string{"mesh", "alt"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, nmDnsmasqDropInFile))
	if err != nil {
		t.Fatalf("read drop-in: %v", err)
	}
	got := string(data)

	for _, want := range []string{
		"# Managed by Mutiauk",
		"server=/mesh/10.200.200.1",
		"server=/alt/10.200.200.1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("drop-in missing %q; full content:\n%s", want, got)
		}
	}
	if reloads.Load() != 1 {
		t.Errorf("expected 1 reload, got %d", reloads.Load())
	}
}

func TestNMDnsmasq_ApplyAtomicRename(t *testing.T) {
	dir, _ := withTempDropInDir(t)
	n := newNMDnsmasqIntegration("10.0.0.1", zap.NewNop())

	for i := 0; i < 5; i++ {
		if err := n.Apply([]string{"mesh"}); err != nil {
			t.Fatalf("Apply iteration %d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() == nmDnsmasqDropInFile {
			continue
		}
		t.Errorf("temp file leaked: %s (rename should clean up)", e.Name())
	}
}

func TestNMDnsmasq_RevertRemovesDropIn(t *testing.T) {
	dir, reloads := withTempDropInDir(t)
	path := filepath.Join(dir, nmDnsmasqDropInFile)
	if err := os.WriteFile(path, []byte("server=/mesh/10.0.0.1\n"), 0o644); err != nil {
		t.Fatalf("seed drop-in: %v", err)
	}

	n := newNMDnsmasqIntegration("10.0.0.1", zap.NewNop())
	if err := n.Revert(); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("drop-in still present after Revert: %v", err)
	}
	if reloads.Load() != 1 {
		t.Errorf("expected 1 reload, got %d", reloads.Load())
	}
}

func TestNMDnsmasq_RevertIdempotentWhenAbsent(t *testing.T) {
	_, reloads := withTempDropInDir(t)
	n := newNMDnsmasqIntegration("10.0.0.1", zap.NewNop())
	if err := n.Revert(); err != nil {
		t.Errorf("Revert when absent should be no-op, got: %v", err)
	}
	if reloads.Load() != 0 {
		t.Errorf("Revert when absent should not trigger reload, got %d", reloads.Load())
	}
}

func TestNMDnsmasq_ApplyEmptyRevertsInstead(t *testing.T) {
	dir, reloads := withTempDropInDir(t)
	path := filepath.Join(dir, nmDnsmasqDropInFile)
	if err := os.WriteFile(path, []byte("server=/mesh/10.0.0.1\n"), 0o644); err != nil {
		t.Fatalf("seed drop-in: %v", err)
	}

	n := newNMDnsmasqIntegration("10.0.0.1", zap.NewNop())
	if err := n.Apply(nil); err != nil {
		t.Fatalf("Apply(nil): %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Apply(nil) should have removed the drop-in, but it's still present: %v", err)
	}
	if reloads.Load() != 1 {
		t.Errorf("expected 1 reload, got %d", reloads.Load())
	}
}

func TestNMDnsmasq_ApplyRequiresListenIP(t *testing.T) {
	withTempDropInDir(t)
	n := newNMDnsmasqIntegration("", zap.NewNop())
	if err := n.Apply([]string{"mesh"}); err == nil {
		t.Error("Apply with empty listenIP should error")
	}
}

func TestNMDnsmasqDropInPath(t *testing.T) {
	dir, _ := withTempDropInDir(t)
	got := NMDnsmasqDropInPath()
	want := filepath.Join(dir, nmDnsmasqDropInFile)
	if got != want {
		t.Errorf("NMDnsmasqDropInPath() = %q, want %q", got, want)
	}
}

func TestNMDnsmasqDropInPresent(t *testing.T) {
	dir, _ := withTempDropInDir(t)
	if nmDnsmasqDropInPresent() {
		t.Error("expected false when no drop-in is present")
	}
	path := filepath.Join(dir, nmDnsmasqDropInFile)
	if err := os.WriteFile(path, []byte("server=/mesh/10.0.0.1\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !nmDnsmasqDropInPresent() {
		t.Error("expected true after seeding the drop-in")
	}
}

func TestNMDnsmasqHostState(t *testing.T) {
	dir, _ := withTempDropInDir(t)

	got, err := NMDnsmasqHostState()
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty content when file absent, got %q", got)
	}

	path := filepath.Join(dir, nmDnsmasqDropInFile)
	body := "server=/mesh/10.0.0.1\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err = NMDnsmasqHostState()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != body {
		t.Errorf("content mismatch: got %q, want %q", got, body)
	}
}

func TestNMDnsmasqIntegration_Name(t *testing.T) {
	withTempDropInDir(t)
	n := newNMDnsmasqIntegration("10.0.0.1", zap.NewNop())
	if n.Name() != IntegrationNMDnsmasq {
		t.Errorf("Name() = %q, want %q", n.Name(), IntegrationNMDnsmasq)
	}
}

func TestRevertAll_RemovesStaleDropIn(t *testing.T) {
	dir, reloads := withTempDropInDir(t)
	path := filepath.Join(dir, nmDnsmasqDropInFile)
	if err := os.WriteFile(path, []byte("server=/mesh/10.0.0.1\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	RevertAll("tun0", zap.NewNop())

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("RevertAll left stale drop-in: %v", err)
	}
	if reloads.Load() != 1 {
		t.Errorf("expected 1 reload from RevertAll, got %d", reloads.Load())
	}
}
