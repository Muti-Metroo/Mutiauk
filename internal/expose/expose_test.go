package expose

import (
	"context"
	"testing"
	"time"

	"github.com/postalsys/mutiauk/internal/config"
	"go.uber.org/zap"
)

// TestManagerRunReloadRace is the regression guard for unsynchronized m.cfg
// reads in the run loop: Reload swaps m.cfg under the manager mutex while the
// loop used to read Enabled/ReconnectMin/ReconnectMax without holding it.
// Meaningful only under -race; before the fix the detector flags the
// concurrent access.
func TestManagerRunReloadRace(t *testing.T) {
	cfg := config.ExposeConfig{
		Enabled:      true,
		ReconnectMin: time.Millisecond,
		ReconnectMax: 5 * time.Millisecond,
	}
	// nil client: runOnce fails fast, so the loop keeps spinning through its
	// config reads while Reload hammers m.cfg from another goroutine.
	m, err := NewManager(cfg, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	m.Start(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			if err := m.Reload(cfg, nil); err != nil {
				t.Errorf("Reload: %v", err)
				return
			}
		}
	}()
	<-done
	m.Stop()
}
