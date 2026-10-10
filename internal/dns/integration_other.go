//go:build !linux && !darwin

package dns

import "go.uber.org/zap"

// On platforms other than Linux and macOS none of the host-DNS backends
// (resolvectl, nm-dnsmasq) are supported, so DetectIntegration falls through
// to the noop implementation.

func resolvectlAvailable() bool { return false }
func nmDnsmasqAvailable() bool  { return false }

func nmDnsmasqDropInPresent() bool { return false }

func newResolvectlIntegration(_, _ string, logger *zap.Logger) Integration {
	return newNoopIntegration(logger)
}

func newNMDnsmasqIntegration(_ string, logger *zap.Logger) Integration {
	return newNoopIntegration(logger)
}
