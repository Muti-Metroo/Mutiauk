//go:build windows

package api

import (
	"net"
	"time"

	"github.com/Microsoft/go-winio"
	"go.uber.org/zap"
)

// pipeSecurityDescriptor restricts the control pipe to LocalSystem (SY)
// and the local Administrators group (BA) with full access (GA). This is
// the Windows equivalent of the Unix socket's 0600 mode: the API exposes
// privileged operations (route mutation, stop/reload) and can surface
// secret-bearing config, so no non-admin local user may connect.
const pipeSecurityDescriptor = "D:P(A;;GA;;;SY)(A;;GA;;;BA)"

// listenEndpoint creates the control listener on a Windows named pipe.
// Named pipes have no filesystem residue and vanish when the owning
// process exits, so there is no stale-socket cleanup to do.
func listenEndpoint(path string, _ *zap.Logger) (net.Listener, error) {
	return winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: pipeSecurityDescriptor,
	})
}

// dialEndpoint connects to the control pipe.
func dialEndpoint(path string, timeout time.Duration) (net.Conn, error) {
	return winio.DialPipe(path, &timeout)
}

// removeEndpoint is a no-op on Windows: named pipes leave no file behind.
func removeEndpoint(_ string) {}
