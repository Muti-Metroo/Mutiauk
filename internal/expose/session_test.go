package expose

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// echoHandler returns the bytes it received once the client closes
// the write side. Used to verify a full DATA->DATA round-trip through
// the multiplexed stream.
type echoHandler struct{}

func (echoHandler) Serve(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	buf, err := io.ReadAll(conn)
	if err != nil {
		return err
	}
	if _, err := conn.Write(buf); err != nil {
		return err
	}
	return nil
}

// fakeAgent is the agent side of an expose session. It speaks the raw
// length-prefixed framing ([u32 len][EncodeFrame bytes]) over a net.Pipe to a
// client Session, exercising the exact wire format the Muti Metroo agent emits.
type fakeAgent struct {
	t    *testing.T
	conn net.Conn
}

// tcpConnPair returns a connected pair of localhost TCP sockets. Unlike
// net.Pipe these are OS-buffered, matching the real SOCKS5 transport: a small
// frame write does not block waiting for the peer to read, so a test can drive
// send/recv from one goroutine without deadlocking.
func tcpConnPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := ln.Accept()
		ch <- res{c, err}
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	r := <-ch
	if r.err != nil {
		t.Fatalf("accept: %v", r.err)
	}
	return client, r.c
}

// newSessionPair wires a client Session to a fakeAgent over a buffered TCP
// pair. The Session runs in the background until the test (or cleanup) closes
// it.
func newSessionPair(t *testing.T, lookup HandlerLookup, keepAlive time.Duration, limits Limits) (*Session, *fakeAgent, chan error) {
	t.Helper()
	c1, c2 := tcpConnPair(t)
	sess := newSession(newRawConn(c1, defaultMaxRead), lookup, keepAlive, limits, zap.NewNop())
	agent := &fakeAgent{t: t, conn: c2}

	runDone := make(chan error, 1)
	go func() { runDone <- sess.Run(context.Background()) }()

	t.Cleanup(func() {
		sess.Close()
		_ = c2.Close()
		select {
		case <-runDone:
		case <-time.After(2 * time.Second):
		}
	})
	return sess, agent, runDone
}

func (a *fakeAgent) send(typ FrameType, id uint64, payload []byte) {
	a.t.Helper()
	frame, err := EncodeFrame(typ, id, payload)
	if err != nil {
		a.t.Fatalf("encode %s: %v", typ, err)
	}
	out := make([]byte, 4+len(frame))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(frame)))
	copy(out[4:], frame)
	_ = a.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if _, err := a.conn.Write(out); err != nil {
		a.t.Fatalf("agent write %s: %v", typ, err)
	}
}

func (a *fakeAgent) sendJSON(typ FrameType, id uint64, v any) {
	a.t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		a.t.Fatalf("marshal %s: %v", typ, err)
	}
	a.send(typ, id, body)
}

// recv reads one frame from the client.
func (a *fakeAgent) recv() Frame {
	a.t.Helper()
	_ = a.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var hdr [4]byte
	if _, err := io.ReadFull(a.conn, hdr[:]); err != nil {
		a.t.Fatalf("recv len: %v", err)
	}
	n := binary.BigEndian.Uint32(hdr[:])
	buf := make([]byte, n)
	if _, err := io.ReadFull(a.conn, buf); err != nil {
		a.t.Fatalf("recv body: %v", err)
	}
	f, err := DecodeFrame(buf)
	if err != nil {
		a.t.Fatalf("decode: %v", err)
	}
	return f
}

// expectAck reads a REGISTER (or UNREGISTER) frame from the client and replies
// with an ACK granting every requested key (or rejecting per the bad map).
func (a *fakeAgent) ackRegister(bad map[string]string) []RegisterEntry {
	a.t.Helper()
	f := a.recv()
	if f.Type != FrameRegister {
		a.t.Fatalf("first frame: got %s want REGISTER", f.Type)
	}
	var rp RegisterPayload
	if err := json.Unmarshal(f.Payload, &rp); err != nil {
		a.t.Fatalf("register payload: %v", err)
	}
	results := make([]AckResult, 0, len(rp.Forwards))
	for _, e := range rp.Forwards {
		r := AckResult{Key: e.Key, OK: true}
		if msg, ok := bad[e.Key]; ok {
			r.OK = false
			r.Error = msg
		}
		results = append(results, r)
	}
	a.sendJSON(FrameRegisterAck, 0, AckPayload{Results: results})
	return rp.Forwards
}

func TestSessionRegisterAndOpenFlow(t *testing.T) {
	sess, agent, _ := newSessionPair(t, func(key string) (Handler, bool) {
		if key == "echo" {
			return echoHandler{}, true
		}
		return nil, false
	}, time.Hour, Limits{})

	// Client REGISTER -> agent ACK -> client ack callback fires.
	gotAck := make(chan AckResult, 1)
	if err := sess.SendRegister(
		[]RegisterEntry{{Key: "echo"}},
		func(key string, ok bool, errMsg string) {
			gotAck <- AckResult{Key: key, OK: ok, Error: errMsg}
		},
	); err != nil {
		t.Fatalf("send register: %v", err)
	}
	if got := agent.ackRegister(nil); len(got) != 1 || got[0].Key != "echo" {
		t.Fatalf("agent saw register: %+v", got)
	}
	select {
	case ack := <-gotAck:
		if !ack.OK || ack.Key != "echo" {
			t.Fatalf("ack: %+v", ack)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no register ack")
	}

	// Agent opens a stream for "echo": OPEN -> OPEN_ACK, DATA in,
	// half-close, expect echoed DATA back, then CLOSE.
	const streamID uint64 = 7
	agent.sendJSON(FrameOpen, streamID, OpenPayload{Key: "echo", RemoteAddr: "1.2.3.4:5"})
	openAck := agent.recv()
	if openAck.Type != FrameOpenAck || openAck.StreamID != streamID {
		t.Fatalf("open_ack: got %s id=%d", openAck.Type, openAck.StreamID)
	}
	var oap OpenAckPayload
	_ = json.Unmarshal(openAck.Payload, &oap)
	if !oap.OK {
		t.Fatalf("open_ack not ok: %s", oap.Error)
	}

	agent.send(FrameData, streamID, []byte("hello"))
	agent.send(FrameCloseWrite, streamID, nil)

	var got bytes.Buffer
	for got.Len() < len("hello") {
		f := agent.recv()
		if f.Type == FrameData && f.StreamID == streamID {
			got.Write(f.Payload)
			continue
		}
		if f.Type == FrameClose {
			break
		}
		t.Fatalf("unexpected frame: %s id=%d", f.Type, f.StreamID)
	}
	if got.String() != "hello" {
		t.Errorf("echo: got %q want %q", got.String(), "hello")
	}
}

func TestSessionRejectsUnknownKey(t *testing.T) {
	sess, agent, _ := newSessionPair(t, func(key string) (Handler, bool) {
		return nil, false
	}, time.Hour, Limits{})

	if err := sess.SendRegister(nil, func(string, bool, string) {}); err != nil {
		t.Fatalf("send register: %v", err)
	}
	agent.ackRegister(nil)

	agent.sendJSON(FrameOpen, 11, OpenPayload{Key: "unknown"})
	ack := agent.recv()
	if ack.Type != FrameOpenAck {
		t.Fatalf("expected OPEN_ACK got %s", ack.Type)
	}
	var oap OpenAckPayload
	_ = json.Unmarshal(ack.Payload, &oap)
	if oap.OK {
		t.Fatal("expected OK=false for unknown key")
	}
	if !strings.Contains(oap.Error, "unknown") {
		t.Errorf("error should mention key: %q", oap.Error)
	}
}

// TestSessionPerKeyStreamCap confirms that the (Max+1)-th OPEN for a single
// key is refused with OPEN_ACK ok=false while existing streams keep running.
func TestSessionPerKeyStreamCap(t *testing.T) {
	sess, agent, _ := newSessionPair(t, func(key string) (Handler, bool) {
		if key == "echo" {
			return echoHandler{}, true
		}
		return nil, false
	}, time.Hour, Limits{MaxStreamsPerKey: 2})

	if err := sess.SendRegister([]RegisterEntry{{Key: "echo"}}, func(string, bool, string) {}); err != nil {
		t.Fatalf("send register: %v", err)
	}
	agent.ackRegister(nil)

	// First two OPENs succeed.
	for i, id := range []uint64{101, 102} {
		agent.sendJSON(FrameOpen, id, OpenPayload{Key: "echo"})
		ack := agent.recv()
		if ack.Type != FrameOpenAck || ack.StreamID != id {
			t.Fatalf("open #%d: got %s id=%d", i, ack.Type, ack.StreamID)
		}
		var oap OpenAckPayload
		_ = json.Unmarshal(ack.Payload, &oap)
		if !oap.OK {
			t.Fatalf("open #%d ok=false: %s", i, oap.Error)
		}
	}

	// Third OPEN must be refused with the cap message.
	agent.sendJSON(FrameOpen, 103, OpenPayload{Key: "echo"})
	ack := agent.recv()
	if ack.Type != FrameOpenAck || ack.StreamID != 103 {
		t.Fatalf("third open: got %s id=%d", ack.Type, ack.StreamID)
	}
	var oap OpenAckPayload
	_ = json.Unmarshal(ack.Payload, &oap)
	if oap.OK {
		t.Fatal("third open should be refused")
	}
	if !strings.Contains(oap.Error, "cap reached") {
		t.Errorf("error should mention cap: %q", oap.Error)
	}
}

// drainResponse reads DATA frames for streamID, accumulating the bytes until a
// CLOSE/CLOSE_WRITE arrives, then parses them as an HTTP/1.1 response.
func drainResponse(t *testing.T, agent *fakeAgent, streamID uint64) *http.Response {
	t.Helper()
	var body bytes.Buffer
	for {
		f := agent.recv()
		switch {
		case f.Type == FrameData && f.StreamID == streamID:
			body.Write(f.Payload)
		case f.Type == FrameCloseWrite && f.StreamID == streamID:
			// response fully written; parse what we have
		case f.Type == FrameClose && f.StreamID == streamID:
			resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(body.Bytes())), nil)
			if err != nil {
				t.Fatalf("parse response: %v (raw=%q)", err, body.String())
			}
			return resp
		default:
			t.Fatalf("unexpected frame while draining: %s id=%d", f.Type, f.StreamID)
		}
	}
}

// TestSessionFolderForward drives a real folderHandler end-to-end through the
// session + raw transport: the agent OPENs a stream, sends an HTTP GET as DATA,
// and reads back the 200 response with the file body.
func TestSessionFolderForward(t *testing.T) {
	dir := t.TempDir()
	const content = "served-over-socks"
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	fh, err := newFolderHandler(dir, zap.NewNop())
	if err != nil {
		t.Fatalf("newFolderHandler: %v", err)
	}

	sess, agent, _ := newSessionPair(t, func(key string) (Handler, bool) {
		if key == "site" {
			return fh, true
		}
		return nil, false
	}, time.Hour, Limits{})

	if err := sess.SendRegister([]RegisterEntry{{Key: "site"}}, func(string, bool, string) {}); err != nil {
		t.Fatalf("send register: %v", err)
	}
	agent.ackRegister(nil)

	const streamID uint64 = 42
	agent.sendJSON(FrameOpen, streamID, OpenPayload{Key: "site", RemoteAddr: "9.9.9.9:1"})
	if ack := agent.recv(); ack.Type != FrameOpenAck {
		t.Fatalf("open_ack: got %s", ack.Type)
	}

	req := "GET /f.txt HTTP/1.1\r\nHost: mesh\r\nConnection: close\r\n\r\n"
	agent.send(FrameData, streamID, []byte(req))

	resp := drainResponse(t, agent, streamID)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != content {
		t.Errorf("body: got %q want %q", got, content)
	}
}

// TestSessionTCPForward drives a real tcpHandler end-to-end through the session:
// the agent OPENs a stream that pipes to a local TCP echo server.
func TestSessionTCPForward(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()

	th := newTCPHandler(ln.Addr().String(), zap.NewNop())
	sess, agent, _ := newSessionPair(t, func(key string) (Handler, bool) {
		if key == "tcp" {
			return th, true
		}
		return nil, false
	}, time.Hour, Limits{})

	if err := sess.SendRegister([]RegisterEntry{{Key: "tcp"}}, func(string, bool, string) {}); err != nil {
		t.Fatalf("send register: %v", err)
	}
	agent.ackRegister(nil)

	const streamID uint64 = 5
	agent.sendJSON(FrameOpen, streamID, OpenPayload{Key: "tcp", RemoteAddr: "8.8.8.8:2"})
	if ack := agent.recv(); ack.Type != FrameOpenAck {
		t.Fatalf("open_ack: got %s", ack.Type)
	}

	agent.send(FrameData, streamID, []byte("round-trip"))
	agent.send(FrameCloseWrite, streamID, nil)

	var got bytes.Buffer
	for got.Len() < len("round-trip") {
		f := agent.recv()
		if f.Type == FrameData && f.StreamID == streamID {
			got.Write(f.Payload)
			continue
		}
		if f.Type == FrameClose || f.Type == FrameCloseWrite {
			break
		}
		t.Fatalf("unexpected frame: %s id=%d", f.Type, f.StreamID)
	}
	if got.String() != "round-trip" {
		t.Errorf("tcp echo: got %q want %q", got.String(), "round-trip")
	}
}

func TestStreamEnforcesReadDeadline(t *testing.T) {
	st := newStream(1, &Session{})
	if err := st.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	start := time.Now()
	_, err := st.Read(buf)
	if err == nil {
		t.Fatal("expected deadline error")
	}
	if time.Since(start) > time.Second {
		t.Errorf("read took too long: %v", time.Since(start))
	}
}

// TestSessionCleanCloseNoError confirms a peer-initiated transport close is
// reported as a clean shutdown (nil error), not a fault.
func TestSessionCleanCloseNoError(t *testing.T) {
	sess, agent, runDone := newSessionPair(t, func(string) (Handler, bool) {
		return nil, false
	}, time.Hour, Limits{})
	_ = sess

	// Close the agent end; the client read loop should return nil.
	_ = agent.conn.Close()
	select {
	case err := <-runDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("expected clean close, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session did not return after peer close")
	}
}

// ensure Session satisfies the call surface the manager expects.
var _ HandlerLookup = (func(string) (Handler, bool))(nil)
var _ AckCallback = (func(string, bool, string))(nil)

var _ = sync.Mutex{}
