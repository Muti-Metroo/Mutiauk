//go:build !windows

package config

// Default filesystem locations on Unix (Linux, macOS). These match the
// historical hardcoded values, so existing installs and tests are
// unaffected.
var (
	// DefaultPath is the canonical config-file location.
	DefaultPath = "/etc/mutiauk/config.yaml"

	defaultPIDFile    = "/var/run/mutiauk.pid"
	defaultSocketPath = "/var/run/mutiauk.sock"
)
