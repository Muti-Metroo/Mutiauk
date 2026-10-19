package dns

import (
	"errors"
	"fmt"

	"go.uber.org/zap"
)

// Integration is implemented by each host-DNS backend (resolvectl,
// nm-dnsmasq, ...) and by the noop fallback.
type Integration interface {
	// Name identifies the backend in logs and `dns status` output.
	Name() string
	// Apply makes the backend route queries for `suffixes` to the
	// resolver. Called whenever the active suffix set changes (the
	// daemon diffs against last-applied state and skips no-ops).
	Apply(suffixes []string) error
	// Revert removes any host-side state previously installed by
	// Apply. Idempotent: safe to call when nothing was applied.
	Revert() error
}

// IntegrationName values used in logs, config, and the diagnostic API.
const (
	IntegrationResolvectl  = "resolvectl"
	IntegrationNMDnsmasq   = "nm-dnsmasq"
	IntegrationEtcResolver = "etc-resolver" // macOS /etc/resolver
	IntegrationNRPT        = "nrpt"         // Windows Name Resolution Policy Table
	IntegrationNone        = "none"
)

// DetectIntegration picks a backend based on `preference`:
//   - "auto" / "" : the first available of resolvectl, nm-dnsmasq
//     (Linux), etc-resolver (macOS), and nrpt (Windows), else noop.
//   - explicit name: that backend if available; error otherwise.
//   - "none": noop.
func DetectIntegration(preference, iface, listenIP string, logger *zap.Logger) (Integration, error) {
	switch preference {
	case "", "auto":
		if resolvectlAvailable() {
			return newResolvectlIntegration(iface, listenIP, logger), nil
		}
		if nmDnsmasqAvailable() {
			return newNMDnsmasqIntegration(listenIP, logger), nil
		}
		if etcResolverAvailable() {
			return newEtcResolverIntegration(listenIP, logger), nil
		}
		if nrptAvailable() {
			return newNRPTIntegration(listenIP, logger), nil
		}
		return newNoopIntegration(logger), nil
	case IntegrationResolvectl:
		if !resolvectlAvailable() {
			return nil, errors.New("dns_integration: resolvectl requested but not available on this host")
		}
		return newResolvectlIntegration(iface, listenIP, logger), nil
	case IntegrationNMDnsmasq:
		if !nmDnsmasqAvailable() {
			return nil, errors.New("dns_integration: nm-dnsmasq requested but not available on this host")
		}
		return newNMDnsmasqIntegration(listenIP, logger), nil
	case IntegrationEtcResolver:
		if !etcResolverAvailable() {
			return nil, errors.New("dns_integration: etc-resolver requested but only available on macOS")
		}
		return newEtcResolverIntegration(listenIP, logger), nil
	case IntegrationNRPT:
		if !nrptAvailable() {
			return nil, errors.New("dns_integration: nrpt requested but only available on Windows")
		}
		return newNRPTIntegration(listenIP, logger), nil
	case IntegrationNone:
		return newNoopIntegration(logger), nil
	default:
		return nil, fmt.Errorf("dns_integration: unknown value %q (want auto, resolvectl, nm-dnsmasq, etc-resolver, nrpt, or none)", preference)
	}
}

// RevertAll runs Revert on every supported backend, ignoring any that
// has nothing to clean. Used by the daemon at startup (to clear stale
// state from a prior crash) and by `mutiauk cleanup`. Errors are
// logged but never returned -- this is best-effort recovery.
func RevertAll(iface string, logger *zap.Logger) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if resolvectlAvailable() {
		if err := newResolvectlIntegration(iface, "", logger).Revert(); err != nil {
			logger.Warn("dns: resolvectl revert failed",
				zap.Error(err), zap.String("iface", iface))
		}
	}
	if nmDnsmasqDropInPresent() {
		// Even when nm-dnsmasq detection now reports unavailable, a
		// stale drop-in left by an earlier run must be removed.
		if err := newNMDnsmasqIntegration("", logger).Revert(); err != nil {
			logger.Warn("dns: nm-dnsmasq revert failed", zap.Error(err))
		}
	}
	if etcResolverPresent() {
		// Remove any /etc/resolver files left by an earlier run.
		if err := newEtcResolverIntegration("", logger).Revert(); err != nil {
			logger.Warn("dns: etc-resolver revert failed", zap.Error(err))
		}
	}
	if nrptPresent() {
		// Remove any NRPT rules left by an earlier run.
		if err := newNRPTIntegration("", logger).Revert(); err != nil {
			logger.Warn("dns: nrpt revert failed", zap.Error(err))
		}
	}
}

// noopIntegration is selected when no host integration is available
// or `dns_integration: none`. It logs a single warning at construction
// time and otherwise stays silent.
type noopIntegration struct {
	logger *zap.Logger
}

func newNoopIntegration(logger *zap.Logger) Integration {
	logger.Warn("no DNS host integration available; configure your resolver manually to forward mesh-host suffixes to the Mutiauk resolver")
	return &noopIntegration{logger: logger}
}

func (n *noopIntegration) Name() string           { return IntegrationNone }
func (n *noopIntegration) Apply(_ []string) error { return nil }
func (n *noopIntegration) Revert() error          { return nil }
