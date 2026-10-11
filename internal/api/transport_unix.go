//go:build !windows

package api

import (
	"net"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
)

// listenEndpoint creates the control listener on a Unix-domain socket.
// The parent directory is created if missing, any stale socket is
// removed, and the socket is chmod'd 0600 (owner-only) because the API
// carries privileged operations (route mutation, stop/reload) and the
// config it can expose holds secrets.
func listenEndpoint(path string, logger *zap.Logger) (net.Listener, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	os.Remove(path) // clear a stale socket left by an unclean shutdown

	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}

	if err := os.Chmod(path, 0o600); err != nil {
		logger.Warn("failed to set socket permissions", zap.Error(err))
	}
	return listener, nil
}

// dialEndpoint connects to the control socket.
func dialEndpoint(path string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", path, timeout)
}

// removeEndpoint deletes the socket file after the listener is closed.
func removeEndpoint(path string) {
	os.Remove(path)
}
