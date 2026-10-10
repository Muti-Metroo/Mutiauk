package config

import (
	"context"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
)

// Watcher monitors config file for changes and triggers reloads
type Watcher struct {
	path      string
	onChange  func(*Config) error
	logger    *zap.Logger
	fsWatcher *fsnotify.Watcher

	mu      sync.RWMutex
	current *Config

	// Debounce settings to avoid multiple reloads for rapid file changes
	debounce time.Duration
}

// NewWatcher creates a new config file watcher
func NewWatcher(path string, logger *zap.Logger, onChange func(*Config) error) (*Watcher, error) {
	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	// Load initial config
	cfg, err := Load(path)
	if err != nil {
		fsWatcher.Close()
		return nil, err
	}

	return &Watcher{
		path:      path,
		onChange:  onChange,
		logger:    logger,
		fsWatcher: fsWatcher,
		current:   cfg,
		debounce:  500 * time.Millisecond,
	}, nil
}

// Watch starts watching the config file for changes
func (w *Watcher) Watch(ctx context.Context) error {
	if err := w.fsWatcher.Add(w.path); err != nil {
		return err
	}

	var (
		debounceTimer *time.Timer
		mu            sync.Mutex
	)

	for {
		select {
		case <-ctx.Done():
			mu.Lock()
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			mu.Unlock()
			w.fsWatcher.Close()
			return ctx.Err()

		case event, ok := <-w.fsWatcher.Events:
			if !ok {
				return nil
			}

			// Only handle write events
			if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) {
				continue
			}

			w.logger.Debug("config file changed", zap.String("op", event.Op.String()))

			// Debounce rapid changes
			mu.Lock()
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(w.debounce, func() {
				w.reload()
			})
			mu.Unlock()

		case err, ok := <-w.fsWatcher.Errors:
			if !ok {
				return nil
			}
			w.logger.Error("config watcher error", zap.Error(err))
		}
	}
}

// reload loads the config and triggers the callback
func (w *Watcher) reload() {
	w.logger.Info("reloading config", zap.String("path", w.path))

	cfg, err := Load(w.path)
	if err != nil {
		w.logger.Error("failed to load config", zap.Error(err))
		return
	}

	if err := cfg.Validate(); err != nil {
		w.logger.Error("invalid config", zap.Error(err))
		return
	}

	w.mu.Lock()
	w.current = cfg
	w.mu.Unlock()

	if w.onChange != nil {
		if err := w.onChange(cfg); err != nil {
			w.logger.Error("failed to apply config", zap.Error(err))
		} else {
			w.logger.Info("config reloaded successfully")
		}
	}
}

// Get returns the current configuration
func (w *Watcher) Get() *Config {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.current
}

// Close stops the watcher
func (w *Watcher) Close() error {
	return w.fsWatcher.Close()
}
