package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Muti-Metroo/Mutiauk/internal/autoroutes"
	"github.com/Muti-Metroo/Mutiauk/internal/route"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestWarnApplyFailureDedupes(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	s, err := New(testConfig(), "", zap.New(core))
	if err != nil {
		t.Fatal(err)
	}

	boom := errors.New("apply had 1 errors: [10.0.0.0/8: boom]")
	s.warnApplyFailure("failed to apply autoroutes", boom)
	s.warnApplyFailure("failed to apply autoroutes", boom)
	if logs.Len() != 1 {
		t.Fatalf("identical failure logged %d times, want 1", logs.Len())
	}

	// A successful apply re-arms: the next failure logs again.
	s.warnApplyFailure("failed to apply autoroutes", nil)
	s.warnApplyFailure("failed to apply autoroutes", boom)
	if logs.Len() != 2 {
		t.Fatalf("failure after success logged %d times total, want 2", logs.Len())
	}

	// A distinct error logs even without an intervening success.
	s.warnApplyFailure("failed to apply autoroutes", errors.New("apply had 1 errors: [10.1.0.0/16: boom]"))
	if logs.Len() != 3 {
		t.Fatalf("distinct failure logged %d times total, want 3", logs.Len())
	}
}

func TestFlushAutoRoutesClearsAutoRoutes(t *testing.T) {
	s, err := New(testConfig(), "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	stubNetwork(t, s)

	s.routeState.setAutoRoutes([]route.Route{{Destination: cidr(t, "10.99.0.0/16"), Enabled: true}})

	s.flushAutoRoutes()

	if got := s.routeState.autoRouteCount(); got != 0 {
		t.Fatalf("autoRouteCount = %d, want 0 after flush", got)
	}
}

func TestWatchStaleAutoRoutesNoSnapshotNoFlush(t *testing.T) {
	s, err := New(testConfig(), "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	stubNetwork(t, s)
	s.routeState.setAutoRoutes([]route.Route{{Destination: cidr(t, "10.99.0.0/16"), Enabled: true}})

	// A watcher that never received a snapshot must not trigger a flush,
	// even with a tiny timeout: there is nothing stale to flush.
	w := autoroutes.NewWatcher(nil, "tun-test", nil, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.watchStaleAutoRoutes(ctx, w, 10*time.Millisecond)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	if got := s.routeState.autoRouteCount(); got != 1 {
		t.Fatalf("autoRouteCount = %d, want 1 (no flush without a snapshot)", got)
	}
}
