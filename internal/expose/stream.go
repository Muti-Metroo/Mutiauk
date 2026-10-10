package expose

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// stream is one multiplexed channel inside an expose session. It
// implements net.Conn so it can drop straight into http.Server.Serve
// (folder handler) or be passed to net.Pipe-style consumers.
//
// The session writes inbound DATA frames into the stream via push,
// and reads outbound bytes via the writer side. Half-close
// (CLOSE_WRITE) shows up as io.EOF on Read; full close drops the
// stream from the session map and unblocks both ends.
type stream struct {
	id      uint64
	session *Session

	// maxBufferedBytes caps the per-stream pending DATA. Snapshotted
	// from the session's Limits at construction so a config reload
	// doesn't change the cap mid-stream (the next stream gets the
	// new value).
	maxBufferedBytes int

	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte // pending payload bytes from peer
	rxEOF  bool   // peer sent CLOSE_WRITE or CLOSE
	closed bool   // local side closed; further reads/writes return errClosed

	// readDeadline is consulted on each Read; zero means no deadline.
	// deadlineTimer wakes the cond when readDeadline elapses; it is
	// owned by SetReadDeadline so Read doesn't allocate a fresh timer
	// per call.
	readDeadline  time.Time
	deadlineTimer *time.Timer
}

// errClosed is returned by Read/Write after the local side closes.
var errClosed = errors.New("expose: stream closed")

// errBufferOverflow fires when push would push the per-stream buffer
// past stream.maxBufferedBytes — a slow consumer with a fast
// producer. The session translates this into a stream close so we
// stop receiving DATA frames for this id.
var errBufferOverflow = errors.New("expose: stream receive buffer overflow")

// defaultMaxBufferedBytes is the fallback when newStream is called
// from a session that wasn't built with explicit limits (e.g. the
// session_test.go literal `&Session{}` cases). Production paths set
// the cap via expose.MaxBufferedBytes.
const defaultMaxBufferedBytes = 16 << 20 // 16 MiB

func newStream(id uint64, session *Session) *stream {
	cap := defaultMaxBufferedBytes
	if session != nil && session.limits.MaxBufferedBytes > 0 {
		cap = session.limits.MaxBufferedBytes
	}
	s := &stream{id: id, session: session, maxBufferedBytes: cap}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// push appends inbound bytes from the peer. Returns errBufferOverflow
// when the per-stream cap would be exceeded; the session uses that
// signal to close the stream and free the id.
func (s *stream) push(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errClosed
	}
	if len(s.buf)+len(data) > s.maxBufferedBytes {
		return errBufferOverflow
	}
	s.buf = append(s.buf, data...)
	s.cond.Broadcast()
	return nil
}

// closeRead marks that the peer will send no more DATA. Pending bytes
// are still drained by Read; once empty, Read returns io.EOF.
func (s *stream) closeRead() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rxEOF = true
	s.cond.Broadcast()
}

// Read implements io.Reader. Blocks until bytes are available, the
// peer half-closes, the local side closes, or the read deadline
// elapses. The deadline wakeup is driven by deadlineTimer (owned by
// SetReadDeadline), not allocated here.
func (s *stream) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for len(s.buf) == 0 && !s.rxEOF && !s.closed {
		if !s.readDeadline.IsZero() && !time.Now().Before(s.readDeadline) {
			return 0, errDeadlineExceeded
		}
		s.cond.Wait()
	}

	if len(s.buf) > 0 {
		n := copy(b, s.buf)
		s.buf = s.buf[n:]
		return n, nil
	}
	if s.rxEOF {
		return 0, io.EOF
	}
	return 0, errClosed
}

// Write sends bytes to the peer as DATA frames on this stream's id.
// The session's write mutex serializes concurrent writers across all
// streams (the expose stream is not concurrent-write-safe).
func (s *stream) Write(b []byte) (int, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return 0, errClosed
	}
	if err := s.session.writeData(s.id, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

// Close notifies the peer (best-effort CLOSE frame) and removes the
// stream from the session map. Idempotent.
func (s *stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()

	s.session.removeStream(s.id)
	_ = s.session.writeControl(FrameClose, s.id, nil)
	return nil
}

// closeLocal tears the stream down without sending a CLOSE frame
// (used when the session itself is shutting down or the peer just
// sent us a CLOSE).
func (s *stream) closeLocal() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.deadlineTimer != nil {
		s.deadlineTimer.Stop()
		s.deadlineTimer = nil
	}
	s.cond.Broadcast()
	s.mu.Unlock()
}

// LocalAddr / RemoteAddr return placeholder net.Addr values. The
// folder handler's http.Server only logs these; the TCP handler
// doesn't use them.
func (s *stream) LocalAddr() net.Addr  { return streamAddr{} }
func (s *stream) RemoteAddr() net.Addr { return streamAddr{} }

func (s *stream) SetDeadline(t time.Time) error {
	return s.SetReadDeadline(t)
}

func (s *stream) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	s.readDeadline = t
	if s.deadlineTimer != nil {
		s.deadlineTimer.Stop()
		s.deadlineTimer = nil
	}
	if !t.IsZero() {
		s.deadlineTimer = time.AfterFunc(time.Until(t), func() {
			s.mu.Lock()
			s.cond.Broadcast()
			s.mu.Unlock()
		})
	}
	s.cond.Broadcast()
	s.mu.Unlock()
	return nil
}

// SetWriteDeadline is a no-op: writes go straight to the expose stream,
// which has its own write timeout enforced at the session level.
func (s *stream) SetWriteDeadline(time.Time) error { return nil }

type streamAddr struct{}

func (streamAddr) Network() string { return "expose" }
func (streamAddr) String() string  { return "expose" }

// errDeadlineExceeded is returned by Read when the read deadline
// elapses before any bytes arrive. Matches net.Error semantics for
// http.Server.
var errDeadlineExceeded = &timeoutError{}

type timeoutError struct{}

func (*timeoutError) Error() string   { return "expose: read deadline exceeded" }
func (*timeoutError) Timeout() bool   { return true }
func (*timeoutError) Temporary() bool { return true }
