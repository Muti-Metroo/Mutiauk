//go:build !windows

package dns

import "go.uber.org/zap"

// The NRPT (Name Resolution Policy Table) backend is Windows-only. On
// every other platform these report unavailable so DetectIntegration
// never selects it and RevertAll skips it.

func nrptAvailable() bool { return false }

func nrptPresent() bool { return false }

func newNRPTIntegration(_ string, logger *zap.Logger) Integration {
	return newNoopIntegration(logger)
}

// NRPTHostState is a no-op on platforms without the NRPT.
func NRPTHostState() (string, error) { return "", nil }
