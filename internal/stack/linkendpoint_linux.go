//go:build linux

package stack

import (
	"context"
	"fmt"

	"github.com/Muti-Metroo/Mutiauk/internal/tun"
	"go.uber.org/zap"
	"gvisor.dev/gvisor/pkg/tcpip/link/fdbased"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// noopStarter is used where the link endpoint runs its own read loop and needs
// no external pump.
type noopStarter struct{}

func (noopStarter) start(context.Context) {}

// newBaseLinkEndpoint wraps the TUN file descriptor with gVisor's fdbased
// endpoint, which dispatches inbound packets and transmits outbound packets on
// its own goroutines. The returned starter is therefore a no-op.
func newBaseLinkEndpoint(tunDev tun.Device, mtu int, _ *zap.Logger) (stack.LinkEndpoint, linkStarter, error) {
	ep, err := fdbased.New(&fdbased.Options{
		FDs:            []int{tunDev.File()},
		MTU:            uint32(mtu),
		EthernetHeader: false, // TUN = Layer 3, no ethernet
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create fdbased endpoint: %w", err)
	}
	return ep, noopStarter{}, nil
}
