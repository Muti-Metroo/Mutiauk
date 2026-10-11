// Package service provides system service management for Mutiauk:
// systemd on Linux and launchd on macOS. Mutiauk requires root privileges
// for TUN interface creation, so only system-level service installation is
// supported (no user-level services).
package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ServiceConfig holds configuration for installing the service.
type ServiceConfig struct {
	// Name is the systemd service name
	Name string

	// Description is the service description
	Description string

	// ConfigPath is the absolute path to the config file
	ConfigPath string

	// WorkingDir is the working directory for the service
	WorkingDir string
}

// DefaultConfig returns a default service configuration.
func DefaultConfig(configPath string) ServiceConfig {
	absPath, _ := filepath.Abs(configPath)
	workDir := filepath.Dir(absPath)

	return ServiceConfig{
		Name:        "mutiauk",
		Description: "Mutiauk TUN-based SOCKS5 proxy agent",
		ConfigPath:  absPath,
		WorkingDir:  workDir,
	}
}

// IsRoot returns true if the current process is running as root.
func IsRoot() bool {
	return isRootImpl()
}

// supportedOS reports whether service management is implemented for the
// current platform (systemd on Linux, launchd on macOS, the Service
// Control Manager on Windows).
func supportedOS() bool {
	return runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows"
}

const errUnsupportedOS = "service management is only supported on Linux, macOS, and Windows"

// Supported reports whether service management is implemented for the
// current platform.
func Supported() bool { return supportedOS() }

// Install installs Mutiauk as a systemd service.
// Requires root privileges.
func Install(cfg ServiceConfig) error {
	if !supportedOS() {
		return errors.New(errUnsupportedOS)
	}

	if !IsRoot() {
		return fmt.Errorf("must run as root to install service")
	}

	// Get the executable path
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Resolve symlinks to get the real path
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("failed to resolve executable path: %w", err)
	}

	return installImpl(cfg, execPath)
}

// Uninstall removes the systemd service.
// Requires root privileges.
func Uninstall(serviceName string) error {
	if !supportedOS() {
		return errors.New(errUnsupportedOS)
	}

	if !IsRoot() {
		return fmt.Errorf("must run as root to uninstall service")
	}

	return uninstallImpl(serviceName)
}

// Status returns the current status of the service.
func Status(serviceName string) (string, error) {
	if !supportedOS() {
		return "", errors.New(errUnsupportedOS)
	}

	return statusImpl(serviceName)
}

// StatusInfo is the structured status of a systemd service unit.
// Suitable for fleet tooling that wants to branch on each field
// rather than parse the human-friendly `Status` string.
type StatusInfo struct {
	Installed bool   `json:"installed"`
	Active    bool   `json:"active"`              // ActiveState == "active"
	Enabled   bool   `json:"enabled"`             // is-enabled returns "enabled"
	SubState  string `json:"sub_state,omitempty"` // e.g. "running", "exited", "dead"
	// ActiveState is the raw systemctl ActiveState value. Kept
	// alongside Active so callers can distinguish "activating" from
	// the binary view.
	ActiveState string `json:"active_state,omitempty"`
}

// StatusDetailed returns the structured status of a systemd service.
// On non-Linux it returns an Installed=false zero value plus the
// usual platform error.
func StatusDetailed(serviceName string) (StatusInfo, error) {
	if !supportedOS() {
		return StatusInfo{}, errors.New(errUnsupportedOS)
	}
	return statusDetailedImpl(serviceName)
}

// IsInstalled checks if the service is already installed.
func IsInstalled(serviceName string) bool {
	if !supportedOS() {
		return false
	}

	return isInstalledImpl(serviceName)
}

// runCommand executes a command and returns combined output.
func runCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}
