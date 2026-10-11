//go:build darwin

package dns

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

// etcResolverDir is the macOS split-DNS directory (resolver(5)). Declared as a
// var so tests can redirect it to a temp dir without touching the real /etc.
var etcResolverDir = "/etc/resolver"

const etcResolverMarker = "# Managed by Mutiauk"

const etcResolverHeader = etcResolverMarker + ". Do not edit by hand.\n" +
	"# Routes DNS queries for this TLD to the local Mutiauk resolver.\n"

// flushDNSCacheFn is the indirection tests use to skip the real cache flush;
// production code keeps the default.
var flushDNSCacheFn = flushDNSCache

// etcResolverIntegration drives macOS split DNS by writing one
// /etc/resolver/<tld> file per active mesh TLD, each pointing the system
// resolver at the local Mutiauk resolver. Files are reconciled to exactly
// the active suffix set on every Apply, and only files carrying our marker are
// ever modified or removed.
type etcResolverIntegration struct {
	listenIP string
	logger   *zap.Logger
}

func newEtcResolverIntegration(listenIP string, logger *zap.Logger) Integration {
	return &etcResolverIntegration{listenIP: listenIP, logger: logger}
}

func (e *etcResolverIntegration) Name() string { return IntegrationEtcResolver }

// Apply reconciles /etc/resolver to the requested suffixes: it writes a
// resolver file for each and removes any of our stale files.
func (e *etcResolverIntegration) Apply(suffixes []string) error {
	if e.listenIP == "" {
		return errors.New("etc-resolver: listenIP is required")
	}
	if err := os.MkdirAll(etcResolverDir, 0o755); err != nil {
		return fmt.Errorf("etc-resolver: create %s: %w", etcResolverDir, err)
	}

	keep := make(map[string]bool, len(suffixes))
	for _, suf := range suffixes {
		tld := strings.ToLower(strings.Trim(strings.TrimSpace(suf), "."))
		if tld == "" {
			continue
		}
		keep[tld] = true
		if err := e.writeResolverFile(tld); err != nil {
			return err
		}
	}

	if err := e.removeManaged(keep); err != nil {
		return err
	}

	flushDNSCacheFn(e.logger)
	e.logger.Info("etc-resolver applied",
		zap.Strings("suffixes", suffixes), zap.String("dir", etcResolverDir))
	return nil
}

// Revert removes every resolver file Mutiauk created.
func (e *etcResolverIntegration) Revert() error {
	if err := e.removeManaged(nil); err != nil {
		return err
	}
	flushDNSCacheFn(e.logger)
	return nil
}

// writeResolverFile writes /etc/resolver/<tld>, refusing to clobber a resolver
// file that we do not own.
func (e *etcResolverIntegration) writeResolverFile(tld string) error {
	path := filepath.Join(etcResolverDir, tld)
	// Defense in depth: suffixes are validated upstream in SetState, but a
	// tld containing path separators would let filepath.Join escape the
	// resolver dir (e.g. "a/../../b" cleans to a path outside it), turning
	// this write into an arbitrary root-owned file write.
	if filepath.Dir(path) != filepath.Clean(etcResolverDir) {
		return fmt.Errorf("etc-resolver: refusing to write outside %s: %q", etcResolverDir, tld)
	}
	if existing, err := os.ReadFile(path); err == nil && !isManagedResolver(existing) {
		return fmt.Errorf("etc-resolver: %s exists and is not managed by Mutiauk", path)
	}
	body := etcResolverHeader + fmt.Sprintf("nameserver %s\nport 53\n", e.listenIP)
	return writeFileAtomic(path, []byte(body), 0o644)
}

// removeManaged deletes our resolver files whose TLD is not in keep. A nil keep
// removes all of them.
func (e *etcResolverIntegration) removeManaged(keep map[string]bool) error {
	return rangeManagedResolvers(func(name, path string, _ []byte) error {
		if keep[name] {
			return nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("etc-resolver: remove %s: %w", path, err)
		}
		return nil
	})
}

// rangeManagedResolvers calls fn for each file in etcResolverDir that carries
// our management marker, passing its base name, full path, and contents. A
// missing directory is treated as empty. fn returning an error stops iteration.
func rangeManagedResolvers(fn func(name, path string, data []byte) error) error {
	entries, err := os.ReadDir(etcResolverDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("etc-resolver: read %s: %w", etcResolverDir, err)
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		path := filepath.Join(etcResolverDir, ent.Name())
		data, err := os.ReadFile(path)
		if err != nil || !isManagedResolver(data) {
			continue // leave files we do not own
		}
		if err := fn(ent.Name(), path, data); err != nil {
			return err
		}
	}
	return nil
}

// etcResolverAvailable is always true on macOS.
func etcResolverAvailable() bool { return true }

// etcResolverPresent reports whether any Mutiauk resolver file exists, so
// RevertAll can clean up after a crash.
func etcResolverPresent() bool {
	found := false
	rangeManagedResolvers(func(_, _ string, _ []byte) error {
		found = true
		return errStopRange
	})
	return found
}

// errStopRange aborts rangeManagedResolvers early without surfacing an error.
var errStopRange = errors.New("stop")

// EtcResolverHostState returns the contents of the resolver files Mutiauk
// manages, for `dns status`.
func EtcResolverHostState() (string, error) {
	var b strings.Builder
	err := rangeManagedResolvers(func(_, path string, data []byte) error {
		fmt.Fprintf(&b, "%s:\n%s", path, string(data))
		return nil
	})
	if err != nil {
		return "", err
	}
	return b.String(), nil
}

func isManagedResolver(data []byte) bool {
	return bytes.Contains(data, []byte(etcResolverMarker))
}

// flushDNSCache best-effort flushes the macOS DNS cache so new resolver files
// take effect immediately.
func flushDNSCache(logger *zap.Logger) {
	if out, err := exec.Command("dscacheutil", "-flushcache").CombinedOutput(); err != nil {
		logger.Debug("etc-resolver: dscacheutil flush failed", zap.Error(err), zap.ByteString("out", out))
	}
	if out, err := exec.Command("killall", "-HUP", "mDNSResponder").CombinedOutput(); err != nil {
		logger.Debug("etc-resolver: mDNSResponder reload failed", zap.Error(err), zap.ByteString("out", out))
	}
}

// writeFileAtomic writes data via a temp file + rename within the same
// directory so concurrent readers never see a partial file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mutiauk.*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// The resolvectl and nm-dnsmasq backends are Linux-only; these report
// unavailable on macOS so DetectIntegration never selects them.

func resolvectlAvailable() bool    { return false }
func nmDnsmasqAvailable() bool     { return false }
func nmDnsmasqDropInPresent() bool { return false }

func newResolvectlIntegration(_, _ string, logger *zap.Logger) Integration {
	return newNoopIntegration(logger)
}

func newNMDnsmasqIntegration(_ string, logger *zap.Logger) Integration {
	return newNoopIntegration(logger)
}
