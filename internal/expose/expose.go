package expose

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Muti-Metroo/Mutiauk/internal/backoff"
	"github.com/Muti-Metroo/Mutiauk/internal/config"
	"github.com/Muti-Metroo/Mutiauk/internal/socks5"
	"go.uber.org/zap"
)

// defaultMaxRead caps a single inbound expose frame (the EncodeFrame bytes:
// header + payload) read off the SOCKS5 stream. Matches the agent's
// ManagerConfig.ReadLimit default; inbound DATA is far smaller in practice.
const defaultMaxRead = HeaderSize + MaxPayloadSize

// SourceConfig and SourceRuntime tag where a forward came from.
const (
	SourceConfig  = "config"
	SourceRuntime = "runtime"
)

// ForwardStatus is one snapshot of a registered forward, for the API.
type ForwardStatus struct {
	Key           string
	Type          string
	Source        string
	Path          string
	Target        string
	Comment       string
	Registered    bool
	Error         string
	ActiveStreams int
}

// Status is the full snapshot exposed to the API + CLI.
type Status struct {
	Enabled          bool
	Connected        bool
	LastError        string
	ConnectedSince   time.Time
	MaxStreamsPerKey int
	Forwards         []ForwardStatus
}

// Manager owns the persistent expose connection over the SOCKS5 endpoint. It
// maintains the set of forwards (config-defined + runtime-added), runs a single
// Session at a time, reconnects with backoff, and re-registers all forwards on
// each reconnect.
type Manager struct {
	logger *zap.Logger

	mu          sync.Mutex
	cfg         config.ExposeConfig
	client      *socks5.Client
	forwards    map[string]*forwardState
	session     *Session // nil when disconnected
	connectedAt time.Time
	lastErr     string

	runCtx    context.Context
	runCancel context.CancelFunc
	runDone   chan struct{}
}

type forwardState struct {
	cfg        config.ForwardConfig
	source     string
	handler    Handler
	registered bool
	error      string
}

// NewManager constructs a Manager from the daemon's current config. The client
// is the shared SOCKS5 client used for the data plane; expose opens a
// CmdExpose stream over it. It does not start any goroutines; call Start.
func NewManager(cfg config.ExposeConfig, client *socks5.Client, logger *zap.Logger) (*Manager, error) {
	m := &Manager{
		logger:   logger.With(zap.String("subsys", "expose")),
		cfg:      cfg,
		client:   client,
		forwards: make(map[string]*forwardState),
	}
	for _, f := range cfg.Forwards {
		if err := m.installForward(f, SourceConfig); err != nil {
			return nil, fmt.Errorf("expose: install %q: %w", f.Key, err)
		}
	}
	return m, nil
}

// Start launches the session loop. Subsequent calls are no-ops.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.runCancel != nil {
		m.mu.Unlock()
		return
	}
	m.runCtx, m.runCancel = context.WithCancel(ctx)
	m.runDone = make(chan struct{})
	runCtx := m.runCtx
	m.mu.Unlock()

	go func() {
		defer close(m.runDone)
		m.run(runCtx)
	}()
}

// Stop signals the session loop to exit and waits for it.
func (m *Manager) Stop() {
	m.mu.Lock()
	cancel := m.runCancel
	done := m.runDone
	m.runCancel = nil
	session := m.session
	m.mu.Unlock()

	if session != nil {
		session.Close()
	}
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// Reload diffs the new config-defined forwards against the current
// set: keys removed from config are dropped (runtime-added keys
// untouched); keys added are installed and registered next reconnect
// or via a live REGISTER if connected.
func (m *Manager) Reload(cfg config.ExposeConfig, client *socks5.Client) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	newKeys := make(map[string]config.ForwardConfig, len(cfg.Forwards))
	for _, f := range cfg.Forwards {
		newKeys[f.Key] = f
	}

	// Drop config-source keys not in the new config.
	var removed []string
	for key, fs := range m.forwards {
		if fs.source != SourceConfig {
			continue
		}
		if _, ok := newKeys[key]; !ok {
			delete(m.forwards, key)
			removed = append(removed, key)
		}
	}

	// Install new or updated config keys.
	var added []RegisterEntry
	for key, fc := range newKeys {
		if existing, ok := m.forwards[key]; ok {
			// Replace if config changed (handler may need rebuild).
			if existing.cfg == fc && existing.source == SourceConfig {
				continue
			}
			// Conflict with a runtime entry: config wins on reload.
			delete(m.forwards, key)
		}
		if err := m.installForwardLocked(fc, SourceConfig); err != nil {
			return fmt.Errorf("expose: reload key %q: %w", key, err)
		}
		added = append(added, RegisterEntry{Key: fc.Key, Comment: fc.Comment})
	}

	m.cfg = cfg
	m.client = client

	// If we're connected, push the diff. Errors only logged: the
	// reload itself succeeded; the next reconnect will re-sync.
	session := m.session
	if session == nil {
		return nil
	}
	if len(removed) > 0 {
		if err := session.SendUnregister(removed, m.makeAckCallback(false)); err != nil {
			m.logger.Warn("expose: live unregister failed", zap.Error(err))
		}
	}
	if len(added) > 0 {
		if err := session.SendRegister(added, m.makeAckCallback(true)); err != nil {
			m.logger.Warn("expose: live register failed", zap.Error(err))
		}
	}
	return nil
}

// Add installs a runtime forward and, if connected, registers it
// immediately. Returns an error if validation fails or the key is
// already in use.
func (m *Manager) Add(f config.ForwardConfig) error {
	if err := config.ValidateForward(f); err != nil {
		return err
	}
	m.mu.Lock()
	if _, exists := m.forwards[f.Key]; exists {
		m.mu.Unlock()
		return fmt.Errorf("expose: key %q already in use", f.Key)
	}
	if err := m.installForwardLocked(f, SourceRuntime); err != nil {
		m.mu.Unlock()
		return err
	}
	session := m.session
	m.mu.Unlock()

	if session != nil {
		if err := session.SendRegister(
			[]RegisterEntry{{Key: f.Key, Comment: f.Comment}},
			m.makeAckCallback(true),
		); err != nil {
			m.logger.Warn("expose: live register failed", zap.String("key", f.Key), zap.Error(err))
		}
	}
	return nil
}

// Remove drops a runtime forward. Errors if the key doesn't exist or
// is config-defined (operator must edit the YAML to remove those).
func (m *Manager) Remove(key string) error {
	m.mu.Lock()
	fs, ok := m.forwards[key]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("expose: key %q not found", key)
	}
	if fs.source == SourceConfig {
		m.mu.Unlock()
		return fmt.Errorf("expose: key %q is config-defined; edit config to remove", key)
	}
	delete(m.forwards, key)
	session := m.session
	m.mu.Unlock()

	if session != nil {
		if err := session.SendUnregister([]string{key}, m.makeAckCallback(false)); err != nil {
			m.logger.Warn("expose: live unregister failed", zap.String("key", key), zap.Error(err))
		}
	}
	return nil
}

// Status returns a copy of the current forward set + connection state.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := Status{
		Enabled:          m.cfg.Enabled,
		Connected:        m.session != nil,
		LastError:        m.lastErr,
		ConnectedSince:   m.connectedAt,
		MaxStreamsPerKey: m.cfg.MaxStreamsPerKey,
	}
	keys := make([]string, 0, len(m.forwards))
	for k := range m.forwards {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var streamsByKey map[string]int
	if m.session != nil {
		streamsByKey = m.session.StreamCountByKey()
	}

	for _, key := range keys {
		fs := m.forwards[key]
		out.Forwards = append(out.Forwards, ForwardStatus{
			Key:           fs.cfg.Key,
			Type:          fs.cfg.Type,
			Source:        fs.source,
			Path:          fs.cfg.Path,
			Target:        fs.cfg.Target,
			Comment:       fs.cfg.Comment,
			Registered:    fs.registered,
			Error:         fs.error,
			ActiveStreams: streamsByKey[key],
		})
	}
	return out
}

// installForward is the unlocked wrapper for callers that already
// hold the manager mutex.
func (m *Manager) installForward(f config.ForwardConfig, source string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.installForwardLocked(f, source)
}

func (m *Manager) installForwardLocked(f config.ForwardConfig, source string) error {
	if _, exists := m.forwards[f.Key]; exists {
		return fmt.Errorf("duplicate key %q", f.Key)
	}
	h, err := m.buildHandler(f)
	if err != nil {
		return err
	}
	m.forwards[f.Key] = &forwardState{cfg: f, source: source, handler: h}
	return nil
}

func (m *Manager) buildHandler(f config.ForwardConfig) (Handler, error) {
	switch f.Type {
	case config.ForwardTypeFolder:
		return newFolderHandler(f.Path, m.logger.With(zap.String("key", f.Key)))
	case config.ForwardTypeTCP:
		return newTCPHandler(f.Target, m.logger.With(zap.String("key", f.Key))), nil
	default:
		return nil, fmt.Errorf("unknown forward type %q", f.Type)
	}
}

func (m *Manager) lookup(key string) (Handler, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fs, ok := m.forwards[key]
	if !ok {
		return nil, false
	}
	return fs.handler, true
}

// makeAckCallback returns a callback that records per-key REGISTER_ACK
// or UNREGISTER_ACK results into forwardState. For unregister, the
// "ok=true" case means the key was successfully dropped, which we
// represent by clearing the registered flag (the key may already be
// removed from the map).
func (m *Manager) makeAckCallback(register bool) AckCallback {
	return func(key string, ok bool, errMsg string) {
		m.mu.Lock()
		defer m.mu.Unlock()
		fs := m.forwards[key]
		if fs == nil {
			return
		}
		if register {
			fs.registered = ok
			if ok {
				fs.error = ""
			} else {
				fs.error = errMsg
			}
			if !ok {
				m.logger.Warn("expose: registration rejected",
					zap.String("key", key),
					zap.String("error", errMsg))
			} else {
				m.logger.Info("expose: registered", zap.String("key", key))
			}
		} else {
			fs.registered = false
		}
	}
}

// run is the dial/register/serve/backoff loop.
func (m *Manager) run(ctx context.Context) {
	// Snapshot the config under the lock: Reload swaps m.cfg concurrently.
	m.mu.Lock()
	enabled := m.cfg.Enabled
	minBackoff := m.cfg.ReconnectMin
	maxBackoff := m.cfg.ReconnectMax
	m.mu.Unlock()

	if !enabled {
		m.logger.Debug("expose: disabled; manager idle")
		return
	}
	backoffDur := minBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		err := m.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			m.setLastError(err.Error())
			m.logger.Warn("expose: session ended", zap.Error(err), zap.Duration("retry_in", backoffDur))
		} else {
			// A session that ran cleanly did connect; reconnect promptly
			// instead of riding whatever backoff earlier failures accrued.
			backoffDur = minBackoff
			m.logger.Info("expose: session ended; reconnecting", zap.Duration("retry_in", backoffDur))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff.Jitter(backoffDur)):
		}
		// Only failed attempts escalate the backoff.
		if err != nil {
			backoffDur *= 2
			if backoffDur > maxBackoff {
				backoffDur = maxBackoff
			}
		}
	}
}

// runOnce opens a CmdExpose stream, registers, and runs one session to
// completion. Returns nil if the session ended cleanly.
func (m *Manager) runOnce(ctx context.Context) error {
	m.mu.Lock()
	client := m.client
	keepalive := m.cfg.KeepalivePeriod
	limits := Limits{
		MaxStreamsPerKey: m.cfg.MaxStreamsPerKey,
		MaxBufferedBytes: m.cfg.MaxBufferedBytes,
	}
	m.mu.Unlock()

	if client == nil {
		return fmt.Errorf("expose: no SOCKS5 client configured")
	}

	m.logger.Info("expose: opening SOCKS5 expose stream", zap.String("server", client.ServerAddr))
	conn, err := client.ExposeStream(ctx)
	if err != nil {
		if socks5.IsCommandNotSupported(err) {
			return fmt.Errorf("agent does not support expose over SOCKS (update the agent): %w", err)
		}
		return err
	}

	session := newSession(newRawConn(conn, defaultMaxRead), m.lookup, keepalive, limits, m.logger)

	m.mu.Lock()
	m.session = session
	m.connectedAt = time.Now()
	m.lastErr = ""
	entries := make([]RegisterEntry, 0, len(m.forwards))
	for _, fs := range m.forwards {
		entries = append(entries, RegisterEntry{Key: fs.cfg.Key, Comment: fs.cfg.Comment})
	}
	m.mu.Unlock()

	m.logger.Info("expose: connected", zap.Int("forwards", len(entries)))

	if len(entries) > 0 {
		if err := session.SendRegister(entries, m.makeAckCallback(true)); err != nil {
			session.Close()
			m.clearSession()
			return fmt.Errorf("send register: %w", err)
		}
	}

	err = session.Run(ctx)
	m.clearSession()
	return err
}

func (m *Manager) clearSession() {
	m.mu.Lock()
	m.session = nil
	for _, fs := range m.forwards {
		fs.registered = false
	}
	m.mu.Unlock()
}

func (m *Manager) setLastError(s string) {
	m.mu.Lock()
	m.lastErr = s
	m.mu.Unlock()
}
