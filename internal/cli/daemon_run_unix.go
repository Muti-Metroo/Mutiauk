//go:build !windows

package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/postalsys/mutiauk/internal/daemon"
	"go.uber.org/zap"
)

// runDaemonLifecycle runs the daemon until it is asked to stop. On Unix
// it wires the process signal set: SIGINT/SIGTERM trigger a graceful
// shutdown (context cancellation) and SIGHUP reloads the on-disk config.
// It returns only after the daemon has fully stopped.
func runDaemonLifecycle(log *zap.Logger, srv *daemon.Server) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go handleDaemonSignals(sigCh, log, srv, cancel)

	if err := srv.Run(ctx); err != nil && err != context.Canceled {
		return fmt.Errorf("daemon error: %w", err)
	}
	return nil
}

// handleDaemonSignals maps Unix signals onto daemon control actions.
// SIGHUP reloads the config (the legacy systemd ExecReload path);
// SIGINT/SIGTERM cancel the run context for a graceful shutdown.
func handleDaemonSignals(sigCh chan os.Signal, log *zap.Logger, srv *daemon.Server, cancel context.CancelFunc) {
	for sig := range sigCh {
		switch sig {
		case syscall.SIGHUP:
			log.Info("received SIGHUP, reloading configuration")
			if err := srv.ReloadFromDisk(); err != nil {
				log.Error("failed to reload config", zap.Error(err))
			}
		case syscall.SIGINT, syscall.SIGTERM:
			log.Info("received shutdown signal", zap.String("signal", sig.String()))
			cancel()
			return
		}
	}
}
