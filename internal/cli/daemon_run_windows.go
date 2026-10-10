//go:build windows

package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/postalsys/mutiauk/internal/daemon"
	"go.uber.org/zap"
	"golang.org/x/sys/windows/svc"
)

// windowsServiceName is the SCM service name the daemon reports under
// when launched by the Service Control Manager. It matches the name used
// by `service install`.
const windowsServiceName = "mutiauk"

// runDaemonLifecycle runs the daemon until it is asked to stop. When the
// process was launched by the Windows Service Control Manager it runs
// under svc.Run and maps SCM Stop/Shutdown controls onto graceful
// context cancellation; run interactively it cancels on Ctrl-C. Config
// reload on Windows is delivered over the control socket (daemon.reload),
// not via an SCM control code.
func runDaemonLifecycle(log *zap.Logger, srv *daemon.Server) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("failed to determine Windows service status: %w", err)
	}
	if isService {
		return svc.Run(windowsServiceName, &windowsService{log: log, srv: srv})
	}
	return runDaemonInteractive(log, srv)
}

// runDaemonInteractive runs the daemon in the foreground, cancelling the
// run context on Ctrl-C (os.Interrupt).
func runDaemonInteractive(log *zap.Logger, srv *daemon.Server) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		<-sigCh
		log.Info("received interrupt, shutting down")
		cancel()
	}()

	if err := srv.Run(ctx); err != nil && err != context.Canceled {
		return fmt.Errorf("daemon error: %w", err)
	}
	return nil
}

// windowsService adapts the daemon to the SCM handler interface.
type windowsService struct {
	log *zap.Logger
	srv *daemon.Server
}

// Execute is the SCM entry point. It starts the daemon in a goroutine,
// reports Running (accepting Stop and Shutdown), and tears down on the
// first stop/shutdown control, waiting for the run goroutine to drain so
// the daemon's deferred cleanup (PID file, TUN adapter, routes, NRPT)
// completes before the service reports Stopped.
func (m *windowsService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown

	changes <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- m.srv.Run(ctx) }()

	changes <- svc.Status{State: svc.Running, Accepts: accepts}

	for {
		select {
		case err := <-runErr:
			// The daemon stopped on its own (e.g. a fatal init error).
			if err != nil && err != context.Canceled {
				m.log.Error("daemon exited with error", zap.Error(err))
				changes <- svc.Status{State: svc.Stopped}
				return false, 1
			}
			changes <- svc.Status{State: svc.Stopped}
			return false, 0
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				m.log.Info("received SCM stop, shutting down")
				// Generous WaitHint so the SCM does not hard-kill the
				// service mid-teardown (which would skip cleanup).
				changes <- svc.Status{State: svc.StopPending, WaitHint: 30000}
				cancel()
				<-runErr // wait for deferred teardown to finish
				changes <- svc.Status{State: svc.Stopped}
				return false, 0
			default:
				m.log.Warn("unexpected SCM control request", zap.Uint32("cmd", uint32(c.Cmd)))
			}
		}
	}
}
