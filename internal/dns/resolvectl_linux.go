//go:build linux

package dns

import (
	"fmt"
	"os/exec"

	"go.uber.org/zap"
)

// resolvectlIntegration drives systemd-resolved via the `resolvectl`
// command-line tool: per-link DNS server pointed at the local
// resolver, plus a routing-only domain (`~suffix`) for each active
// mesh TLD so non-mesh names stay on the system's normal upstream.
type resolvectlIntegration struct {
	iface    string
	listenIP string
	logger   *zap.Logger
}

func newResolvectlIntegration(iface, listenIP string, logger *zap.Logger) Integration {
	return &resolvectlIntegration{iface: iface, listenIP: listenIP, logger: logger}
}

func (r *resolvectlIntegration) Name() string { return IntegrationResolvectl }

func (r *resolvectlIntegration) Apply(suffixes []string) error {
	if len(suffixes) == 0 {
		return r.Revert()
	}
	if err := configureResolvectl(r.iface, r.listenIP, suffixes); err != nil {
		return err
	}
	r.logger.Info("systemd-resolved routed mesh suffixes to local resolver",
		zap.String("iface", r.iface),
		zap.Strings("suffixes", suffixes),
	)
	return nil
}

func (r *resolvectlIntegration) Revert() error {
	if r.iface == "" {
		return nil
	}
	if err := revertResolvectl(r.iface); err != nil {
		return err
	}
	r.logger.Info("systemd-resolved per-link DNS reverted",
		zap.String("iface", r.iface),
	)
	return nil
}

func resolvectlAvailable() bool {
	_, err := exec.LookPath("resolvectl")
	return err == nil
}

// configureResolvectl runs:
//
//	resolvectl dns    <iface> <listenIP>
//	resolvectl domain <iface> '~suf1' '~suf2' ...
//
// resolvectl's domain command replaces the per-link domain list, so
// passing the full active suffix set in one call applies adds and
// removes atomically.
func configureResolvectl(iface, listenIP string, suffixes []string) error {
	if iface == "" || listenIP == "" {
		return fmt.Errorf("resolvectl: iface and listenIP are required")
	}
	if len(suffixes) == 0 {
		return fmt.Errorf("resolvectl: at least one suffix is required (use Revert to clear)")
	}
	if out, err := exec.Command("resolvectl", "dns", iface, listenIP).CombinedOutput(); err != nil {
		return fmt.Errorf("resolvectl dns %s %s: %w (%s)", iface, listenIP, err, string(out))
	}
	args := make([]string, 0, 2+len(suffixes))
	args = append(args, "domain", iface)
	for _, suf := range suffixes {
		args = append(args, "~"+suf)
	}
	if out, err := exec.Command("resolvectl", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("resolvectl domain %s %v: %w (%s)", iface, suffixes, err, string(out))
	}
	return nil
}

// revertResolvectl unbinds the per-interface DNS settings configured
// by configureResolvectl. Safe to call on an interface that no longer
// exists.
func revertResolvectl(iface string) error {
	if iface == "" {
		return nil
	}
	if out, err := exec.Command("resolvectl", "revert", iface).CombinedOutput(); err != nil {
		return fmt.Errorf("resolvectl revert %s: %w (%s)", iface, err, string(out))
	}
	return nil
}
