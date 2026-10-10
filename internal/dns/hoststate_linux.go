//go:build linux

package dns

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ResolvectlHostState returns a short, human-readable snapshot of the
// per-link DNS configuration systemd-resolved holds for `iface`. Used
// by `mutiauk dns status` to compare what the daemon thinks it
// applied against what the host actually shows. Returns an empty
// string and a nil error when resolvectl is unavailable.
func ResolvectlHostState(iface string) (string, error) {
	if !resolvectlAvailable() {
		return "", nil
	}
	if iface == "" {
		return "", errors.New("iface is required")
	}
	out, err := exec.Command("resolvectl", "status", iface).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("resolvectl status %s: %w (%s)", iface, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// NMDnsmasqHostState returns the contents of the mutiauk-managed
// dnsmasq drop-in if it exists, or an empty string when no drop-in is
// present. Used by `mutiauk dns status`.
func NMDnsmasqHostState() (string, error) {
	path := filepath.Join(nmDnsmasqDropInDir, nmDnsmasqDropInFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(data), nil
}

// NMDnsmasqDropInPath returns the canonical drop-in path. Used by the
// CLI to print where it looked.
func NMDnsmasqDropInPath() string {
	return filepath.Join(nmDnsmasqDropInDir, nmDnsmasqDropInFile)
}
