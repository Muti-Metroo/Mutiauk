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

	"go.uber.org/zap"
)

const (
	nmDnsmasqDropInFile  = "mutiauk.conf"
	nmDnsmasqRuntimeConf = "/run/NetworkManager/dnsmasq.conf" // exists only when NM is using the dnsmasq plugin

	nmDnsmasqHeader = "# Managed by Mutiauk. Do not edit by hand.\n" +
		"# Lines below tell NetworkManager's dnsmasq plugin to forward\n" +
		"# DNS queries for each mesh TLD to the local Mutiauk resolver.\n"
)

// nmDnsmasqDropInDir is the directory NetworkManager scans for
// dnsmasq plugin drop-ins. Declared as a var so tests can redirect it
// to a temp dir without touching the real /etc.
var nmDnsmasqDropInDir = "/etc/NetworkManager/dnsmasq.d"

// reloadNetworkManagerFn is the indirection tests use to skip the
// real systemctl call; production code keeps the default.
var reloadNetworkManagerFn = reloadNetworkManager

// nmDnsmasqIntegration drives NetworkManager's dnsmasq plugin by
// writing a per-suffix forwarder drop-in to nmDnsmasqDropInDir and
// signalling NM to reload. The drop-in is rewritten atomically (temp
// file + rename within the same directory) and reloads are best-effort.
type nmDnsmasqIntegration struct {
	listenIP string
	logger   *zap.Logger
}

func newNMDnsmasqIntegration(listenIP string, logger *zap.Logger) Integration {
	return &nmDnsmasqIntegration{listenIP: listenIP, logger: logger}
}

func (n *nmDnsmasqIntegration) Name() string { return IntegrationNMDnsmasq }

func (n *nmDnsmasqIntegration) Apply(suffixes []string) error {
	if len(suffixes) == 0 {
		return n.Revert()
	}
	if n.listenIP == "" {
		return errors.New("nm-dnsmasq: listenIP is required")
	}

	body := nmDnsmasqHeader
	for _, suf := range suffixes {
		body += fmt.Sprintf("server=/%s/%s\n", suf, n.listenIP)
	}

	if err := writeFileAtomic(filepath.Join(nmDnsmasqDropInDir, nmDnsmasqDropInFile), []byte(body), 0o644); err != nil {
		return fmt.Errorf("nm-dnsmasq: write drop-in: %w", err)
	}

	if err := reloadNetworkManagerFn(); err != nil {
		// File is on disk; NM will pick it up on the next state change.
		// Don't fail the whole apply.
		n.logger.Warn("nm-dnsmasq: reload NetworkManager failed; drop-in is on disk and will be picked up on next NM event",
			zap.Error(err),
		)
	}

	n.logger.Info("nm-dnsmasq drop-in applied",
		zap.String("path", filepath.Join(nmDnsmasqDropInDir, nmDnsmasqDropInFile)),
		zap.Strings("suffixes", suffixes),
	)
	return nil
}

func (n *nmDnsmasqIntegration) Revert() error {
	path := filepath.Join(nmDnsmasqDropInDir, nmDnsmasqDropInFile)
	err := os.Remove(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("nm-dnsmasq: remove %s: %w", path, err)
	}
	if err == nil {
		if rerr := reloadNetworkManagerFn(); rerr != nil {
			n.logger.Warn("nm-dnsmasq: reload NetworkManager after revert failed", zap.Error(rerr))
		}
		n.logger.Info("nm-dnsmasq drop-in removed", zap.String("path", path))
	}
	return nil
}

// nmDnsmasqAvailable reports whether NetworkManager's dnsmasq plugin
// is the active DNS backend. The strongest signal is the runtime
// config file the plugin generates; fall back to parsing
// `NetworkManager --print-config` (which prints the merged config
// across NetworkManager.conf + every conf.d/ drop-in).
func nmDnsmasqAvailable() bool {
	if _, err := exec.LookPath("nmcli"); err != nil {
		return false
	}
	if _, err := os.Stat(nmDnsmasqRuntimeConf); err == nil {
		return true
	}
	return nmDnsmasqConfigured()
}

// nmDnsmasqConfigured returns true if `dns=dnsmasq` appears in the
// `[main]` section of NetworkManager's merged config.
func nmDnsmasqConfigured() bool {
	out, err := exec.Command("NetworkManager", "--print-config").Output()
	if err != nil {
		return false
	}
	inMain := false
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inMain = strings.EqualFold(line, "[main]")
			continue
		}
		if !inMain {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok &&
			strings.EqualFold(strings.TrimSpace(k), "dns") &&
			strings.EqualFold(strings.TrimSpace(v), "dnsmasq") {
			return true
		}
	}
	return false
}

// nmDnsmasqDropInPresent reports whether a drop-in file exists at the
// canonical path. Used by RevertAll to clean up after a crash even if
// the dnsmasq backend is no longer the active one.
func nmDnsmasqDropInPresent() bool {
	_, err := os.Stat(filepath.Join(nmDnsmasqDropInDir, nmDnsmasqDropInFile))
	return err == nil
}

// reloadNetworkManager sends NetworkManager a SIGHUP via systemctl,
// which makes it re-read dnsmasq.d/*.conf and signal its dnsmasq
// child. Falls back to a direct kill -HUP via the pidfile if
// systemctl is unavailable.
func reloadNetworkManager() error {
	if _, err := exec.LookPath("systemctl"); err == nil {
		out, err := exec.Command("systemctl", "reload", "NetworkManager").CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl reload NetworkManager: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	pidData, err := os.ReadFile("/run/NetworkManager/NetworkManager.pid")
	if err != nil {
		return fmt.Errorf("nm reload: %w", err)
	}
	if out, err := exec.Command("kill", "-HUP", strings.TrimSpace(string(pidData))).CombinedOutput(); err != nil {
		return fmt.Errorf("kill -HUP NetworkManager: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
