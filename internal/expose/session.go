package expose

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// Handler serves one accepted stream. Implemented by folderHandler
// and tcpHandler.
type Handler interface {
	Serve(ctx context.Context, conn net.Conn) error
}

// HandlerLookup returns the handler registered for a routing key.
// Returns ok=false when no handler is currently registered (the
// session replies with OPEN_ACK error and closes the stream).
type HandlerLookup func(key string) (Handler, bool)

// AckCallback is invoked once per result in a REGISTER_ACK or
// UNREGISTER_ACK frame. The Manager uses it to update per-key state
// (registered=true on success, error string on failure).
type AckCallback func(key string, ok bool, errMsg string)

// Limits holds the per-session DoS guards. Zero values are treated
// as defaults at the call site (see DefaultLimits) so tests that
// build a session inline don't have to spell them out.
type Limits struct {
	// MaxStreamsPerKey caps concurrent inbound streams sharing one
	// routing key. The (Max+1)-th OPEN is refused with OPEN_ACK
	// error; existing streams are unaffected.
	MaxStreamsPerKey int
	// MaxBufferedBytes caps the per-stream receive buffer. Hitting
	// the cap closes the stream cleanly.
	MaxBufferedBytes int
}

// DefaultLimits returns the defaults applied when a Session is built
// without explicit limits (notably from tests). Production callers
// should always pass values from ExposeConfig.
func DefaultLimits() Limits {
	return Limits{
		MaxStreamsPerKey: 256,
		MaxBufferedBytes: 16 << 20,
	}
}

// Session owns one expose-transport lifetime. Run blocks until the transport
// closes (peer drop, network error, ctx cancel, or local Close).
type Session struct {
	conn      messageConn
	logger    *zap.Logger
	lookup    HandlerLookup
	keepAlive time.Duration
	limits    Limits

	// writeMu serializes writes (the transport is not concurrent-write-
	// safe) and protects writeClosed.
	writeMu     sync.Mutex
	writeClosed bool

	streamMu  sync.Mutex
	streams   map[uint64]*stream
	keyByID   map[uint64]string // stream_id -> routing key, for per-key stats
	keyCounts map[string]int    // routing key -> live stream count

	// pong tracks last received PONG nonce time. The keepalive loop
	// reads it after sending a PING; if it doesn't advance within the
	// grace window, the session terminates.
	pongMu     sync.Mutex
	lastPongAt time.Time
	pingNonce  atomic.Uint64

	// onAck is set per-call by SendRegister/SendUnregister. Reads on
	// the frame loop dispatch to the active callback. registerMu
	// serializes register/unregister send pairs so the ack callback
	// always matches the most recent send.
	registerMu        sync.Mutex
	ackMu             sync.Mutex
	pendingRegister   AckCallback
	pendingUnregister AckCallback

	// closeOnce ensures Close is idempotent.
	closeOnce sync.Once
	closed    chan struct{}
}

// newSession wraps an already-established expose transport. The session takes
// ownership of conn and will close it on Run return or Close.
//
// Zero-valued fields in `limits` are filled from DefaultLimits, so
// callers that don't care about caps (typically tests) can pass
// Limits{}.
func newSession(conn messageConn, lookup HandlerLookup, keepAlive time.Duration, limits Limits, logger *zap.Logger) *Session {
	defaults := DefaultLimits()
	if limits.MaxStreamsPerKey <= 0 {
		limits.MaxStreamsPerKey = defaults.MaxStreamsPerKey
	}
	if limits.MaxBufferedBytes <= 0 {
		limits.MaxBufferedBytes = defaults.MaxBufferedBytes
	}
	return &Session{
		conn:       conn,
		logger:     logger,
		lookup:     lookup,
		keepAlive:  keepAlive,
		limits:     limits,
		streams:    make(map[uint64]*stream),
		keyByID:    make(map[uint64]string),
		keyCounts:  make(map[string]int),
		closed:     make(chan struct{}),
		lastPongAt: time.Now(),
	}
}

// Run drives the read loop and keepalive until the expose stream closes.
// Returns nil for a clean close, an error otherwise.
func (s *Session) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	keepDone := make(chan struct{})
	go func() {
		defer close(keepDone)
		s.runKeepalive(ctx)
	}()

	err := s.runReader(ctx)

	// Reader exited: shut everything down.
	s.shutdown()
	cancel()
	<-keepDone
	return err
}

// Close shuts the session down. Safe to call from any goroutine.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.writeMu.Lock()
		s.writeClosed = true
		s.writeMu.Unlock()
		_ = s.conn.Close()
	})
}

// shutdown closes every active stream and the underlying transport.
// Safe to call multiple times.
func (s *Session) shutdown() {
	s.streamMu.Lock()
	for _, st := range s.streams {
		st.closeLocal()
	}
	s.streams = make(map[uint64]*stream)
	s.keyByID = make(map[uint64]string)
	s.keyCounts = make(map[string]int)
	s.streamMu.Unlock()
	s.Close()
}

// runReader is the main dispatch loop. Each transport message is one frame;
// we decode and act per type. Returns on read error, peer close, or context
// cancel.
func (s *Session) runReader(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		data, err := s.conn.ReadMessage()
		if err != nil {
			if isClean(err) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("expose: read: %w", err)
		}
		frame, err := DecodeFrame(data)
		if err != nil {
			return fmt.Errorf("expose: decode frame: %w", err)
		}
		if err := s.dispatch(ctx, frame); err != nil {
			return err
		}
	}
}

func (s *Session) dispatch(ctx context.Context, f Frame) error {
	switch f.Type {
	case FrameRegisterAck:
		return s.handleAck(f, true)
	case FrameUnregisterAck:
		return s.handleAck(f, false)
	case FrameOpen:
		return s.handleOpen(ctx, f)
	case FrameData:
		s.handleData(f)
	case FrameCloseWrite:
		s.handleCloseWrite(f)
	case FrameClose:
		s.handleClose(f)
	case FramePing:
		// Echo PONG with the same nonce.
		return s.writeControl(FramePong, 0, f.Payload)
	case FramePong:
		s.notePong()
	case FrameRegister, FrameUnregister, FrameOpenAck:
		// These are server-bound or client-bound only; ignore.
		s.logger.Debug("expose: unexpected frame from server", zap.Stringer("type", f.Type))
	default:
		s.logger.Debug("expose: unknown frame type", zap.Stringer("type", f.Type))
	}
	return nil
}

func (s *Session) handleAck(f Frame, register bool) error {
	var payload AckPayload
	if err := json.Unmarshal(f.Payload, &payload); err != nil {
		return fmt.Errorf("expose: bad ack payload: %w", err)
	}
	s.ackMu.Lock()
	cb := s.pendingRegister
	if !register {
		cb = s.pendingUnregister
	}
	if register {
		s.pendingRegister = nil
	} else {
		s.pendingUnregister = nil
	}
	s.ackMu.Unlock()
	if cb == nil {
		s.logger.Debug("expose: ack with no pending callback",
			zap.Bool("register", register),
			zap.Int("results", len(payload.Results)))
		return nil
	}
	for _, r := range payload.Results {
		cb(r.Key, r.OK, r.Error)
	}
	return nil
}

func (s *Session) handleOpen(ctx context.Context, f Frame) error {
	var payload OpenPayload
	if err := json.Unmarshal(f.Payload, &payload); err != nil {
		// Refuse the stream cleanly so the server frees its id.
		_ = s.writeOpenAck(f.StreamID, false, "bad open payload")
		return nil
	}

	handler, ok := s.lookup(payload.Key)
	if !ok {
		_ = s.writeOpenAck(f.StreamID, false, fmt.Sprintf("no handler for key %q", payload.Key))
		return nil
	}

	st := newStream(f.StreamID, s)
	s.streamMu.Lock()
	if _, exists := s.streams[f.StreamID]; exists {
		s.streamMu.Unlock()
		_ = s.writeOpenAck(f.StreamID, false, "duplicate stream id")
		return nil
	}
	// Per-key concurrent-stream cap: refuse OPEN once we'd exceed
	// the limit so a runaway peer can't exhaust file descriptors.
	// Existing streams keep running; the operator sees the refusal
	// in the daemon log and via `expose status`.
	if cur := s.keyCounts[payload.Key]; cur >= s.limits.MaxStreamsPerKey {
		s.streamMu.Unlock()
		s.logger.Warn("expose: per-key stream cap reached, refusing OPEN",
			zap.String("key", payload.Key),
			zap.Int("active", cur),
			zap.Int("max", s.limits.MaxStreamsPerKey),
		)
		_ = s.writeOpenAck(f.StreamID, false,
			fmt.Sprintf("per-key concurrent-stream cap reached (%d)", s.limits.MaxStreamsPerKey))
		return nil
	}
	s.streams[f.StreamID] = st
	s.keyByID[f.StreamID] = payload.Key
	s.keyCounts[payload.Key]++
	s.streamMu.Unlock()

	if err := s.writeOpenAck(f.StreamID, true, ""); err != nil {
		s.removeStream(f.StreamID)
		st.closeLocal()
		return err
	}

	go s.serveStream(ctx, payload, handler, st)
	return nil
}

func (s *Session) serveStream(ctx context.Context, open OpenPayload, h Handler, st *stream) {
	logger := s.logger.With(
		zap.String("key", open.Key),
		zap.Uint64("stream_id", st.id),
		zap.String("remote", open.RemoteAddr),
	)
	logger.Debug("expose: stream open")
	start := time.Now()

	err := h.Serve(ctx, st)

	// Always tear down: send CLOSE if still alive, drop from map.
	st.Close()

	logger.Debug("expose: stream closed",
		zap.Duration("duration", time.Since(start)),
		zap.Error(err),
	)
}

func (s *Session) handleData(f Frame) {
	s.streamMu.Lock()
	st := s.streams[f.StreamID]
	s.streamMu.Unlock()
	if st == nil {
		return // stream already closed; drop bytes
	}
	if err := st.push(f.Payload); err != nil {
		s.logger.Warn("expose: closing stream",
			zap.Uint64("stream_id", f.StreamID),
			zap.Error(err))
		st.Close()
	}
}

func (s *Session) handleCloseWrite(f Frame) {
	s.streamMu.Lock()
	st := s.streams[f.StreamID]
	s.streamMu.Unlock()
	if st == nil {
		return
	}
	st.closeRead()
}

func (s *Session) handleClose(f Frame) {
	s.streamMu.Lock()
	st := s.streams[f.StreamID]
	s.dropStreamLocked(f.StreamID)
	s.streamMu.Unlock()
	if st == nil {
		return
	}
	st.closeRead()
	st.closeLocal()
}

func (s *Session) notePong() {
	s.pongMu.Lock()
	s.lastPongAt = time.Now()
	s.pongMu.Unlock()
}

// runKeepalive sends a PING every keepAlive period and bails out if
// the peer doesn't PONG within keepAlive*2.
func (s *Session) runKeepalive(ctx context.Context) {
	if s.keepAlive <= 0 {
		return
	}
	ticker := time.NewTicker(s.keepAlive)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.closed:
			return
		case <-ticker.C:
			nonce := s.pingNonce.Add(1)
			payload := make([]byte, 8)
			binary.BigEndian.PutUint64(payload, nonce)
			if err := s.writeControl(FramePing, 0, payload); err != nil {
				s.logger.Debug("expose: ping write failed", zap.Error(err))
				s.Close()
				return
			}
			s.pongMu.Lock()
			last := s.lastPongAt
			s.pongMu.Unlock()
			if time.Since(last) > 2*s.keepAlive+5*time.Second {
				s.logger.Warn("expose: peer not responding to PING; closing",
					zap.Duration("since_last_pong", time.Since(last)))
				s.Close()
				return
			}
		}
	}
}

// SendRegister sends a REGISTER frame and routes the next REGISTER_ACK
// to cb. Only one register-in-flight is allowed; concurrent calls
// queue on registerMu.
func (s *Session) SendRegister(entries []RegisterEntry, cb AckCallback) error {
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	s.ackMu.Lock()
	s.pendingRegister = cb
	s.ackMu.Unlock()
	return s.writeJSON(FrameRegister, 0, RegisterPayload{Forwards: entries})
}

func (s *Session) SendUnregister(keys []string, cb AckCallback) error {
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	s.ackMu.Lock()
	s.pendingUnregister = cb
	s.ackMu.Unlock()
	return s.writeJSON(FrameUnregister, 0, UnregisterPayload{Keys: keys})
}

// maxDataChunk caps a single DATA frame's payload. The raw transport wraps the
// 9-byte-header frame (HeaderSize + payload) in a length prefix, and the agent
// caps each inbound wrapped frame at its ReadLimit (default MaxPayloadSize), so
// the payload must leave room for the header to avoid a "frame too large"
// rejection.
const maxDataChunk = MaxPayloadSize - HeaderSize

// writeData fragments large writes into maxDataChunk chunks and
// sends them as DATA frames. Called from stream.Write.
func (s *Session) writeData(streamID uint64, b []byte) error {
	for len(b) > 0 {
		n := len(b)
		if n > maxDataChunk {
			n = maxDataChunk
		}
		frame, err := EncodeFrame(FrameData, streamID, b[:n])
		if err != nil {
			return err
		}
		if err := s.writeRaw(frame); err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

func (s *Session) writeControl(typ FrameType, streamID uint64, payload []byte) error {
	frame, err := EncodeFrame(typ, streamID, payload)
	if err != nil {
		return err
	}
	return s.writeRaw(frame)
}

func (s *Session) writeJSON(typ FrameType, streamID uint64, payload any) error {
	frame, err := MarshalJSONFrame(typ, streamID, payload)
	if err != nil {
		return err
	}
	return s.writeRaw(frame)
}

func (s *Session) writeOpenAck(id uint64, ok bool, errMsg string) error {
	return s.writeJSON(FrameOpenAck, id, OpenAckPayload{OK: ok, Error: errMsg})
}

// writeRaw serializes the actual transport write. Returns an error if
// the session is shutting down.
func (s *Session) writeRaw(frame []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.writeClosed {
		return errors.New("expose: session closed")
	}
	return s.conn.WriteMessage(frame)
}

func (s *Session) removeStream(id uint64) {
	s.streamMu.Lock()
	s.dropStreamLocked(id)
	s.streamMu.Unlock()
}

// dropStreamLocked deletes the stream id from all bookkeeping maps
// and decrements the per-key live count. Caller must hold streamMu.
func (s *Session) dropStreamLocked(id uint64) {
	if _, ok := s.streams[id]; !ok {
		return
	}
	delete(s.streams, id)
	if key, ok := s.keyByID[id]; ok {
		delete(s.keyByID, id)
		if s.keyCounts[key] > 0 {
			s.keyCounts[key]--
		}
		if s.keyCounts[key] == 0 {
			delete(s.keyCounts, key)
		}
	}
}

// StreamCountByKey returns a snapshot of live stream counts grouped
// by routing key. Used by the manager for per-forward status.
func (s *Session) StreamCountByKey() map[string]int {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	out := make(map[string]int, len(s.keyCounts))
	for k, v := range s.keyCounts {
		out[k] = v
	}
	return out
}

// isClean reports whether a read error represents an orderly transport close
// rather than a fault. On the raw SOCKS5 stream a peer drop or local Close
// surfaces as io.EOF / io.ErrClosedPipe / net.ErrClosed.
func isClean(err error) bool {
	if err == nil {
		return true
	}
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed)
}
