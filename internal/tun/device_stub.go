//go:build !linux && !darwin && !windows

package tun

import (
	"fmt"
	"runtime"
)

// New returns an error on unsupported platforms (anything other than
// Linux, macOS, and Windows).
func New(cfg Config) (Device, error) {
	return nil, fmt.Errorf("TUN device is only supported on Linux, macOS, and Windows, current platform: %s", runtime.GOOS)
}
