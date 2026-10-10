//go:build !linux && !darwin && !windows

package service

import "fmt"

// isRootImpl returns false on unsupported platforms.
func isRootImpl() bool {
	return false
}

// installImpl returns an error on unsupported platforms.
func installImpl(cfg ServiceConfig, execPath string) error {
	return fmt.Errorf(errUnsupportedOS)
}

// uninstallImpl returns an error on unsupported platforms.
func uninstallImpl(serviceName string) error {
	return fmt.Errorf(errUnsupportedOS)
}

// statusImpl returns an error on unsupported platforms.
func statusImpl(serviceName string) (string, error) {
	return "", fmt.Errorf(errUnsupportedOS)
}

// statusDetailedImpl returns a zero StatusInfo on unsupported platforms.
func statusDetailedImpl(serviceName string) (StatusInfo, error) {
	return StatusInfo{}, fmt.Errorf(errUnsupportedOS)
}

// isInstalledImpl returns false on unsupported platforms.
func isInstalledImpl(serviceName string) bool {
	return false
}
