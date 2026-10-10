//go:build !darwin

package dns

import "go.uber.org/zap"

// The /etc/resolver backend is macOS-only. On every other platform these hooks
// report unavailable so DetectIntegration and RevertAll skip it.

func etcResolverAvailable() bool { return false }

func etcResolverPresent() bool { return false }

func newEtcResolverIntegration(_ string, logger *zap.Logger) Integration {
	return newNoopIntegration(logger)
}

// EtcResolverHostState is a no-op on platforms without /etc/resolver.
func EtcResolverHostState() (string, error) { return "", nil }
