//go:build !linux && !darwin && !windows

package stack

import (
	"fmt"
	"runtime"

	"github.com/postalsys/mutiauk/internal/tun"
	"go.uber.org/zap"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// newBaseLinkEndpoint is unsupported on platforms other than Linux and macOS.
func newBaseLinkEndpoint(_ tun.Device, _ int, _ *zap.Logger) (stack.LinkEndpoint, linkStarter, error) {
	return nil, nil, fmt.Errorf("network stack is only supported on Linux and macOS, current platform: %s", runtime.GOOS)
}
