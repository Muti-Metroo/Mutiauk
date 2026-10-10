# Expose over SOCKS5 - Mutiauk (client) Implementation Plan

Status: IMPLEMENTED (client + agent shipped; unit + cross-repo integration
tests green). The dashboard/HTTP cleanup in section 7 remains a separate
follow-up. ASCII only.

Follows the precedent set by the route-watch / snapshot work that is already
shipped in both repos.

The agent side is DONE and merged in `../Muti Metroo` (commit 201434d, "serve expose
over SOCKS5 CmdExpose, retire /api/expose websocket"). That commit is
BREAKING: the `/api/expose` websocket route, handler, and bearer gate were
DELETED. There is NO websocket fallback on the agent anymore. Consequences for
this repo:
- Mutiauk's current websocket-based expose (`internal/expose/dialer.go` +
  `Manager.runOnce` dialing `autoroutes.URL`) now dials a route that no longer
  exists -> expose is BROKEN against the updated agent until this change lands.
- Capability miss (SOCKS reply REP=0x07) now means "agent too old", NOT "use
  the websocket". There is nothing to fall back to; treat it as an error.

Agent wire contract to match (from commit 201434d):
- Command: `CmdExpose = 0x07`. Standard SOCKS5 handshake + request; reply
  `REP=0x00` success, `REP=0x07` when the agent has no expose handler.
- After the reply the connection is a full-duplex expose stream framed as
  `[len:u32 BE][ EncodeFrame bytes ]`, where the inner bytes are the existing
  9-byte-header frame (`[u8 type][u64 stream_id BE][payload]`). See the agent's
  `internal/expose/transport.go` (`rawConn`) and `internal/socks5/expose.go`.
- The agent caps each inbound wrapped message at `ManagerConfig.ReadLimit`,
  DEFAULT 1 MiB, measured over the `EncodeFrame` bytes (9-byte header +
  payload). So a client DATA frame's payload must stay <= ReadLimit - 9. Our
  `MaxPayloadSize` is 1 MiB and `writeData` chunks at `MaxPayloadSize`
  (`session.go:432`); reduce the raw-transport chunk to `MaxPayloadSize - 9`
  (or set the chunk to the agent default 1 MiB - 9) so a max-size frame is not
  rejected. The agent emits DATA at most 16 KiB (`MaxDataPayload`), so the
  inbound direction is comfortably within our cap.
- Frame type bytes, `FrameHeaderSize`/`HeaderSize` (9), and the JSON payload
  shapes are IDENTICAL across the two repos (verified) - no parser changes.

Match the agent's transport seam shape for easy cross-review: the agent uses a
byte-oriented `messageConn { ReadMessage() ([]byte,error); WriteMessage([]byte)
error; Close() error }` with NO context parameter (teardown is driven by
`Close()` unblocking a blocked read). Mirror that here rather than a
frame/ctx-typed interface.

## 1. Goal

Today `expose` is the LAST consumer of `autoroutes.url` + `autoroutes.token`.
After route-watch moved route/host state onto the SOCKS5 endpoint, expose
still dials a SEPARATE websocket at `autoroutes.URL` (`wss://host/api/expose`)
authenticated with `autoroutes.token`:

- `internal/expose/expose.go:397` `Manager.runOnce` builds `dialOptions{
  BaseURL: m.autoroutes.URL, Token: m.autoroutes.Token, ... }`.
- `internal/expose/dialer.go` dials the websocket with a bearer header.
- `internal/config/config.go:563` `Validate` REQUIRES `autoroutes.url` when
  expose is enabled.

Goal: carry expose over the same SOCKS5 endpoint as everything else, so a
deployment needs ONLY `socks5.server`. Once this lands, `autoroutes.url`,
`autoroutes.token`, and `MUTI_METROO_TOKEN` have no remaining consumer and the
dashboard/HTTP control plane can be deleted entirely.

## 2. Why this is a contained change

The expose wire protocol is already transport-agnostic by design
(`internal/expose/protocol.go`):

```
[u8 type][u64 stream_id big-endian][payload...]
```

The ONLY coupling to websocket is that a websocket delivers each frame as
its own message. Concretely, in `internal/expose/session.go`:
- `runReader` (line ~174): `typ, data, err := s.conn.Read(ctx)` -> one
  message == one frame -> `DecodeFrame(data)`.
- `writeRaw` (line ~472): `s.conn.Write(ctx, websocket.MessageBinary, frame)`
  -> one frame == one message.
- `Close` (line ~147), `shutdown`, and `isClean` (line ~520) call
  websocket-specific methods.

A raw SOCKS5 connection is a byte stream with no message boundaries, so the
frame needs an explicit length prefix on that transport. This is EXACTLY the
codec already used by route-watch (`internal/socks5/routewatch.go`,
`RouteWatchStream.Next`: read `[type:1][len:u32 BE][payload]`).

## 3. Design: a transport seam in the expose package

Introduce the SAME byte-oriented seam the agent uses (`../Muti Metroo`
`internal/expose/transport.go`) so `Session` no longer references
`*websocket.Conn`. Put it in a new file `internal/expose/transport.go`:

```go
// messageConn moves exactly one whole expose message (the EncodeFrame
// output: 9-byte header + payload) per call. The implementation delimits
// frames on the wire; Session never sees partial frames. No context
// parameter: teardown is driven by Close() unblocking a blocked read,
// mirroring the route-watch handler and the agent's messageConn.
type messageConn interface {
    ReadMessage() ([]byte, error)
    WriteMessage(b []byte) error
    Close() error
}
```

One implementation is needed now (`rawConn`); a websocket impl is NOT needed
because the agent retired its websocket endpoint.

`rawConn` wraps the SOCKS5 `net.Conn`. Wire framing reuses the EXISTING
9-byte-header frame as the inner unit and adds only a u32 length prefix -
byte-identical to the agent's `rawConn`:

```
[u32 BE inner-length][ EncodeFrame bytes: type(1)+stream_id(8)+payload ]
```

- `WriteMessage(b)` -> write `len(b)` as u32 BE then `b`, in ONE `Write` so a
  partial frame cannot interleave (matches the agent).
- `ReadMessage()` -> `io.ReadFull` 4 bytes -> bound against a max
  (`maxRead`, set from the agent default 1 MiB) -> `io.ReadFull` N -> return
  bytes; the Session then `DecodeFrame`s them.

This keeps `EncodeFrame`/`DecodeFrame` (`protocol.go`) UNCHANGED.

Refactor `Session` (`internal/expose/session.go`):
- Change field `conn *websocket.Conn` -> `conn messageConn`.
- `runReader`: replace `s.conn.Read(ctx)` + message-type check with
  `b, err := s.conn.ReadMessage()` then `DecodeFrame(b)`.
- `writeRaw(frame []byte)`: replace `s.conn.Write(ctx, MessageBinary, frame)`
  with `s.conn.WriteMessage(frame)`. The serialization mutex (`writeMu`) and
  `writeClosed` logic stay.
- `writeData` (`session.go:432`): chunk at `MaxPayloadSize - HeaderSize` so a
  max-size DATA frame's wrapped length stays within the agent's ReadLimit.
- `Close`/`shutdown` call `s.conn.Close()`.
- `isClean`: drop the websocket close-status interpretation; treat `io.EOF` +
  `ctx.Err()` as a clean close.
- `newSession` takes a `messageConn`.

Keepalive (PING/PONG), the stream map, per-key caps, register/ack
dispatch, and all `Handler` logic stay IDENTICAL - they already operate on
decoded `Frame`s.

## 4. New SOCKS5 command + client method

- `internal/socks5/protocol.go:28` - add `CmdExpose byte = 0x07` after
  `CmdSnapshot = 0x06`.
- `internal/socks5/client.go` - add
  `func (c *Client) ExposeStream(ctx) (net.Conn, error)` modeled on
  `RouteWatch` (`internal/socks5/routewatch.go:78`): `dialServer` (handles
  tcp + ws transports) -> `handshake` -> `sendRequest(conn, CmdExpose,
  routeWatchDummyAddr())` -> clear deadline -> return the RAW `net.Conn`.
  Unlike `RouteWatch` (read-only `RouteWatchStream`), expose needs the full
  duplex conn because the client both reads (OPEN/DATA from agent) and writes
  (OPEN_ACK/DATA/REGISTER to agent). Reuse `IsCommandNotSupported`
  (`routewatch.go:150`) for capability detection.

## 5. Manager wiring (`internal/expose/expose.go`)

`Manager.runOnce` (line ~397) currently always dials the websocket. Replace
the websocket dial with a SOCKS5 expose stream (no transport switch needed -
the agent has only one transport now):

```
conn, err := socksClient.ExposeStream(ctx)
if IsCommandNotSupported(err):
    log "agent does not support expose over SOCKS (update the agent)"; back off
else if err: back off
else:
    t := newRawConn(conn, defaultMaxRead)
    session := newSession(t, m.lookup, keepalive, limits, logger)
    session.Run(ctx)
```

- The `Manager` needs a handle to the shared `*socks5.Client` (the daemon
  already constructs one for the data plane). Pass it into the expose
  `Manager` constructor in place of the `autoroutes` URL/token struct.
- Backoff, register/unregister-on-reconnect, `Add`/`Remove`/`Status`/`Reload`
  are unchanged.
- No websocket fallback: the agent deleted `/api/expose`, so a REP=0x07 is a
  hard "agent too old" error, surfaced in logs and `expose status`.

## 6. Auth model change

The websocket uses `autoroutes.token` as a bearer (`dialer.go:42`). Over
SOCKS5 the connection authenticates via the existing SOCKS5 handshake
(none or username/password) - identical to how route-watch is gated
(`../Muti Metroo/AUTOROUTES_OVER_SOCKS_PLAN.md` 4.7). So:
- No bearer token needed for expose once on SOCKS.
- Registered routing keys become tied to the SOCKS5 connection's mesh
  identity instead of `http.token_hash`. Document this in the operator docs
  and the example config.

## 7. Config changes (`internal/config/config.go`)

- Relax `Validate` (line ~563): expose no longer requires `autoroutes.url`;
  require `socks5.server` instead (already required for the data plane).
- After this lands and the agent is confirmed deployed, the cleanup commit
  removes:
  - `AutoRoutes.URL`, `AutoRoutes.Token`, and the `MUTI_METROO_TOKEN` env hook
    (`config.go:266`).
  - `autoroutes.source` `dashboard` option and the `auto` HTTP fallback.
  - `internal/autoroutes/poller.go` (HTTP poller) and the HTTP bits of
    `internal/autoroutes/client.go` (`FetchDashboard`, HTTP `LookupPath`,
    `NewClient`). KEEP `DashboardResponse`/`DashboardRoute`/
    `DashboardMeshHost`/`ProcessDashboard`/`FilterRoutes`/
    `LookupPathFromSnapshot` - these are the shared data model used by the
    SOCKS path (consider renaming off "Dashboard" in a separate cosmetic
    pass).
  - `internal/expose/dialer.go` (websocket dial) - delete; the agent has no
    websocket endpoint, so no websocket `messageConn` impl is kept.
  - dashboard branches in `internal/daemon/server.go`
    (`startDashboardPoller`, `autoPoller`, fallback) and
    `internal/cli/trace.go` (HTTP `LookupPath` path; trace already supports
    `CmdSnapshot`).

  NOTE: do the cleanup as a SEPARATE commit AFTER expose-over-socks is
  verified end-to-end, so a rollback path exists.

## 8. Tests

- `transport_test.go`: round-trip a message through `rawConn` over a
  `net.Pipe`; assert length-prefix framing, max-frame rejection, partial
  read handling (mirror the agent's `transport_test.go`).
- `session_test.go`: run the existing session tests over the `rawConn`
  transport (net.Pipe) so OPEN/DATA/CLOSE/PING/PONG, per-key caps, and ack
  dispatch are covered.
- `expose.go` manager: `ExposeStream` capability miss -> fallback path (when
  url set) and disabled path (when not).
- Integration (Docker): bring up the agent with expose-over-socks, register a
  folder + tcp forward, drive a request through the mesh, assert bytes.

## 9. Effort (rough)

- Transport seam + raw codec: ~120-160 lines (codec mirrors route-watch).
- SOCKS command + client method: ~40 lines.
- Manager wiring + config: ~60 lines.
- Tests: ~150 lines.
Total ~one focused day, plus the separate cleanup commit. The bottleneck is
the agent side (separate repo / session).

## 10. File index (this repo)

- `internal/socks5/protocol.go` - command constants (`CmdSnapshot = 0x06`)
- `internal/socks5/client.go` - `dialServer`, `handshake`, `sendRequest`;
  add `ExposeStream`
- `internal/socks5/routewatch.go` - length-prefix codec + `RouteWatch` +
  `IsCommandNotSupported` to mirror
- `internal/expose/protocol.go` - `Frame`, `EncodeFrame`, `DecodeFrame`
  (reused unchanged)
- `internal/expose/session.go` - `Session.conn`, `runReader`, `writeRaw`,
  `Close`, `isClean` -> transport seam
- `internal/expose/dialer.go` - current websocket dial (delete)
- `internal/expose/expose.go` - `Manager.runOnce`, `dialOptions`,
  constructor -> open `ExposeStream` + socks client handle
- `internal/expose/transport.go` - NEW: `messageConn`, `rawConn`
  (byte-oriented, mirrors `../Muti Metroo/internal/expose/transport.go`)
- `internal/config/config.go` - expose validation (~563), `AutoRoutesConfig`
- `internal/daemon/server.go` - expose Manager construction; pass socks
  client
- `internal/cli/trace.go` - already uses `CmdSnapshot`; HTTP path removable
