# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

**Note:** The current year is 2026. Keep documentation and references up to date accordingly.

## Project Overview

Mutiauk is a TUN-based SOCKS5 proxy agent for Linux, macOS, and Windows. It creates a virtual network interface (TUN) and routes selected TCP/UDP/ICMP traffic through a SOCKS5 proxy using Google's gVisor userspace TCP/IP stack. The project is a single Go binary with two modes: daemon (traffic forwarding) and CLI (route management, host integration, diagnostics). The TUN backend is per-OS: `/dev/net/tun` on Linux, utun on macOS, and the embedded Wintun driver on Windows.

## Build Commands

```bash
# NOTE: the vendored gVisor does not compile under Go 1.26. The Makefile
# pins GOTOOLCHAIN=go1.25.5 for the darwin/windows cross-compile targets
# only; on a host whose default Go is 1.26+, prefix local builds and tests
# the same way:
export GOTOOLCHAIN=go1.25.5

# Build per platform
make build-linux          # linux/amd64
make build-darwin         # darwin/arm64 (pins GOTOOLCHAIN itself)
make build-windows-arm64  # windows/arm64 (embeds Wintun DLL)
make build-all            # every supported target

# Build for current platform
make build

# Build + scp the windows/arm64 binary to the test host (10.8.0.13)
make deploy-windows-test

# Run all tests
make test

# Run specific package tests
go test -v ./internal/socks5/...

# Run single test
go test -v -run TestName ./internal/socks5/

# Lint (requires golangci-lint)
make lint

# Format code
make fmt

# Tidy dependencies
make tidy
```

## Development Environment

**Always use Docker for building and testing** unless explicitly requested otherwise. TUN requires Linux with CAP_NET_ADMIN, so Docker is essential for development on macOS.

```bash
# Start full test environment (Kali + SOCKS5 proxy + nginx target)
make docker-test

# Get shell in Kali container for manual testing
make docker-shell

# View logs
make logs
```

## Code Style

- **ASCII only**: Do not use emojis or non-ASCII characters in code, comments, documentation, commit messages, or any other text files.

## Code Quality

After completing large changes (new features, significant refactoring, or adding multiple files), use the `code-simplifier` agent to review and simplify the affected code. This helps:
- Eliminate code duplication
- Extract shared logic into reusable helpers
- Improve separation of concerns
- Maintain consistent patterns across the codebase

Run the simplifier on specific packages:
```
Use code-simplifier to simplify internal/<package>/. Focus on consolidating duplication and extracting shared patterns.
```

Always verify changes with `make test` in Docker after simplification.

## Architecture

### Data Flow

```
TCP:    Application -> TUN -> gVisor Stack -> TCP Forwarder -> SOCKS5 CONNECT -> Target
UDP:    Application -> TUN -> Intercept Endpoint -> Raw UDP Forwarder -> SOCKS5 UDP ASSOCIATE -> Target
ICMP:   Application -> TUN -> ICMP Forwarder (echo via SOCKS5 UDP) -> Target
Expose: Mesh peer -> Muti Metroo agent listener -> Muti Metroo -> SOCKS5 CmdExpose stream -> Mutiauk -> static folder | local TCP target
```

### Core Components

**daemon/server.go** - Orchestrator that wires all components together. Entry point is `Server.Run()` which:
1. Creates TUN device
2. Initializes gVisor stack with TCP/UDP/ICMP handlers
3. Sets up NAT table for connection tracking
4. Applies routes via netlink
5. Starts the API server (Unix socket)
6. When `admin.enabled` (default): starts the local admin HTTP interface (`internal/adminhttp`) on loopback
7. When `autoroutes.enabled`: starts the local DNS resolver, picks a host DNS integration, and starts the autoroutes poller (which drives routes + suffixes + hosts atomically each tick)
8. When `expose.enabled`: starts the expose Manager, which opens a `CmdExpose` (0x07) stream over the SOCKS5 endpoint (`socks5.server`) and registers configured forwards
9. Handles SIGHUP for hot reload (autoroutes routes + DNS + expose forward set are all rebuilt against the new config)

**adminhttp/** - Local admin HTTP interface. A thin HTTP layer over `api.StateProvider` (the same interface backing the Unix-socket control plane; the daemon `*Server` satisfies it), so it adds no business logic. `server.go` owns the lifecycle (loopback bind on `admin.listen`, best-effort dual-bind of the other IP family on the same port, graceful `Shutdown`). `handlers.go` serves an embedded dashboard at `GET /` plus JSON endpoints (`/api/status`, `/api/agent`, `/api/routes`, `/api/expose`, `POST /api/expose`, `DELETE /api/expose/{key}`). The dashboard (`index.html`, embedded via `go:embed`) is built on Bootstrap 5.3 using standard components (navbar, cards, tables, badges, `dl.row`, forms, Toast, Modal); Bootstrap CSS+JS is vendored under `internal/adminhttp/static/` and embedded (`go:embed static`, served at `GET /static/` via `http.FileServerFS`), so the UI has no CDN / runtime network dependency and works air-gapped. Security (`auth.go`): every endpoint, dashboard included, requires the per-install bearer token from `token.go` (file `admin-token`, 0600, next to the config; created on first daemon start); browser login goes through a `?token=` GET that sets a SameSite=Strict HttpOnly cookie and redirects (`mutiauk admin` prints that URL). All requests must carry a loopback Host header, and POST/DELETE require `Content-Type: application/json`. `config.Validate` enforces the loopback bind.

**stack/stack.go** - Wraps gVisor's `tcpip/stack` package. Uses `tcp.NewForwarder` to intercept TCP connections, converts them to `net.Conn` via `gonet`, then passes to handlers. UDP is handled separately via link-layer interception.

**stack/intercept_endpoint.go** - Wraps gVisor's link endpoint to intercept UDP packets at the link layer. This is necessary because gVisor's `udp.NewForwarder` only handles packets destined for local addresses, not outbound traffic. The intercept endpoint parses raw IP/UDP headers and passes payloads to the `RawUDPHandler`.

**proxy/tcp_forwarder.go** - Implements `TCPHandler` interface. Establishes SOCKS5 CONNECT and performs bidirectional data copy. Supports `PreConnect` for accurate port scanning.

**proxy/udp_forwarder.go** - Legacy UDP handler using gVisor's UDP forwarder (for local-destined packets).

**proxy/raw_udp_forwarder.go** - Implements `RawUDPHandler` interface for intercepted UDP. Uses SOCKS5 UDP ASSOCIATE to relay datagrams. Maintains a pool of relay sessions with TTL-based cleanup.

**proxy/icmp_forwarder.go** - Forwards ICMP echo requests/replies through the SOCKS5 UDP relay so `ping` works through the tunnel.

**socks5/client.go** - SOCKS5 client supporting CONNECT (TCP) and UDP ASSOCIATE. Handles authentication (none or username/password).

**nat/table.go** - Connection tracking with TTL-based expiration. Maps (protocol, src, dst) -> proxy connection. Background GC goroutine cleans stale entries.

**route/manager.go** - Linux route management via netlink. Supports idempotent add/remove, plan/diff for declarative route management, conflict detection for overlapping CIDRs.

**tun/device_linux.go** - TUN device creation using `/dev/net/tun` with `TUNSETIFF` ioctl. Non-Linux platforms get a stub that returns an error.

**config/watcher.go** - Hot reload via fsnotify with debouncing.

**dns/server.go** - In-process DNS resolver bound to the TUN IPv4 on port 53. Started when `autoroutes.enabled` is true. Resolves names that match any of the active TLD suffixes (e.g. `.mesh`) against the agent's mesh-host registry; everything else is refused so the system resolver falls through. The autoroutes poller fetches the suffix set + host list from the agent dashboard and applies it to the DNS server atomically with each route refresh. `SetState` validates agent-supplied suffixes against the strict DNS-label charset (lowercase alnum + interior hyphens, no empty labels, max 253 chars) and drops invalid entries with a Warn, since suffixes are written into root-owned host config (dnsmasq drop-ins, `/etc/resolver` files, resolvectl argv, NRPT).

**dns/integration.go** - Pluggable host-DNS integration. Defines the `Integration` interface (`Name`, `Apply(suffixes)`, `Revert`), the `DetectIntegration(preference, iface, listenIP, logger)` constructor, and `RevertAll` (idempotent best-effort revert across every backend, used by the daemon at startup and by `mutiauk cleanup`). Backends:
- **dns/resolvectl_linux.go** - `resolvectlIntegration`. Runs `resolvectl dns/domain` to give systemd-resolved a per-link mesh resolver + routing-only `~suffix` domains.
- **dns/nmdnsmasq_linux.go** - `nmDnsmasqIntegration`. Writes `/etc/NetworkManager/dnsmasq.d/mutiauk.conf` with one `server=/<tld>/<resolver-ip>` line per active TLD, then reloads NetworkManager. Atomic write via temp + rename.
- **noop** (file-private) - selected when neither is available or `dns_integration: none`. Logs once at construction.

The daemon's `applyDNSIntegration` keys a diff cache on `(listenIP, suffixes)` so `Apply` only fires on actual change; quiet polls skip the integration entirely.

**dns/hoststate_linux.go** - `ResolvectlHostState(iface)` and `NMDnsmasqHostState()` read host-side artifacts (resolvectl per-link config, the dnsmasq drop-in) for the `mutiauk dns status` diagnostic.

**autoroutes/poller.go** - Periodic poll of the Muti Metroo agent dashboard. Pulls routes, host registry, and TLD suffixes; applies them atomically. Backed by `autoroutes/client.go`, which sends `Authorization: Bearer <token>` (from config or `MUTI_METROO_TOKEN` env var) when the agent has `http.token_hash` set.

**expose/** - Publishes local services to the mesh over the SOCKS5 endpoint (the same `socks5.server` used for the data plane), via the Muti Metroo `CmdExpose` (0x07) stream. Each routing key is registered on the agent; inbound mesh forward streams for that key are piped back over the stream and served locally as either a strict static folder or a TCP forward. Authorization is the SOCKS5 handshake; no separate URL or bearer token.
- **expose/protocol.go** - Wire format: `[u8 type][u64 stream_id][payload]`. Frame types: `REGISTER`/`REGISTER_ACK`, `UNREGISTER`/`UNREGISTER_ACK`, `OPEN`/`OPEN_ACK`, `DATA`, `CLOSE_WRITE`, `CLOSE`, `PING`/`PONG`. Only the agent opens streams (operator side is firewalled). Max payload 1 MiB per frame.
- **expose/transport.go** - `messageConn` transport seam under a Session (`ReadMessage`/`WriteMessage`/`Close`, no context). `rawConn` frames each expose message over the raw SOCKS5 stream with a u32 big-endian length prefix (`[len:u32 BE][EncodeFrame bytes]`), mirroring the agent and the route-watch framing.
- **expose/session.go** - One transport (SOCKS5 stream) lifetime: read loop dispatches frames, write mutex serializes, keepalive loop PINGs every `keepalive_period`, drops the connection if no PONG within 2x. DATA writes chunk at `MaxPayloadSize - HeaderSize` so the wrapped frame stays within the agent's read limit. Tracks per-key live-stream counts for diagnostics and enforces `expose.max_streams_per_key` (default 256): the (Max+1)-th OPEN for a single routing key is refused with OPEN_ACK error so a misbehaving peer cannot exhaust file descriptors.
- **expose/stream.go** - One multiplexed channel per inbound connection; implements `net.Conn`. Per-stream receive buffer cap (default 16 MiB; configurable via `expose.max_buffered_bytes`); the protocol has no flow control, so overflow closes the stream. Single-shot deadline timer owned by `SetReadDeadline` so `Read` doesn't allocate per call.
- **expose/handler_folder.go** - Strict static file server. Drives HTTP/1.1 directly on the multiplexed `net.Conn` (no `http.Server` to keep per-stream lifecycle simple). Path validation: `path.Clean` + reject traversal + `EvalSymlinks` + `filepath.Rel` to confirm the resolved path stays under the configured root. 404 returns the stock Apache 404 body without the `<address>` banner; no `Server` header. Files are streamed via `io.Copy` (no full read into memory). Includes `Date` header.
- **expose/handler_tcp.go** - `net.Dial` to the configured target, bidirectional `io.Copy`, half-close propagation.
- **expose/expose.go** - `Manager` owns the configured + runtime forward set plus the shared `*socks5.Client`, runs the open-stream/register/serve/backoff loop (via `client.ExposeStream`), and exposes `Add`/`Remove`/`Status`/`Reload` to the daemon. Backoff is jittered exponential between `expose.reconnect_min` and `reconnect_max`. A `ReplyCommandNotSupported` from the agent is a hard "agent too old" error (no fallback). Live `REGISTER`/`UNREGISTER` frames are issued for runtime mutations and config-reload diffs without restarting the session.

### Key Interfaces

```go
// stack/stack.go - Implemented by proxy forwarders
type TCPHandler interface {
    HandleTCP(ctx context.Context, conn net.Conn, srcAddr, dstAddr net.Addr) error
}

// TCPPreConnector - Optional interface for accurate port scanning
type TCPPreConnector interface {
    PreConnect(ctx context.Context, srcAddr, dstAddr net.Addr) (net.Conn, error)
}

// RawUDPHandler - For link-layer intercepted UDP packets
type RawUDPHandler interface {
    HandleRawUDP(ctx context.Context, srcIP, dstIP net.IP, srcPort, dstPort uint16, payload []byte) ([]byte, error)
}

// dns/integration.go - Implemented by each host-DNS backend
type Integration interface {
    Name() string                  // "resolvectl" | "nm-dnsmasq" | "none"
    Apply(suffixes []string) error
    Revert() error                 // idempotent
}

// expose/session.go - Implemented by folderHandler and tcpHandler
type Handler interface {
    Serve(ctx context.Context, conn net.Conn) error
}
```

## Platform Constraints

- **Linux, macOS, and Windows**: each has its own TUN backend and route/DNS/service seams behind shared interfaces (see the `_linux.go` / `_darwin.go` / `_windows.go` / `_stub.go` files). Other platforms get stubs that return "unsupported".
  - Linux: `/dev/net/tun`, netlink routes, systemd, resolvectl/nm-dnsmasq DNS.
  - macOS: utun (4-byte AF header), `/sbin/route` + PF_ROUTE, launchd, `/etc/resolver` DNS.
  - Windows: embedded Wintun driver (raw IP), iphlpapi routes via winipcfg, the Service Control Manager, NRPT DNS. The control plane is a named pipe (not a Unix socket); paths default under `%ProgramData%\Mutiauk`. See `WINDOWS_SUPPORT_PLAN.md`.
- **Elevated privileges required**: TUN creation and route manipulation need root (CAP_NET_ADMIN) on Unix or Administrator on Windows.
- **Control plane**: `daemon reload`/`stop` prefer the API socket/pipe on all platforms (the `daemon.reload`/`daemon.stop`/`daemon.ping` methods); Unix signals (SIGHUP/SIGTERM) remain a Linux/macOS fallback. Windows has no signals.
- **gVisor dependency**: Uses `gvisor.dev/gvisor/pkg/tcpip` for userspace networking. Builds for all platforms with `GOTOOLCHAIN=go1.25.5` (gVisor does not compile under Go 1.26).

## Configuration

Config file: YAML format, see `configs/mutiauk.example.yaml`

Key sections:
- `daemon`: PID file, API socket path, optional health port, `status_probe_target` (host:port that `mutiauk status` opens through the SOCKS5 tunnel; default `example.com:80`, override for air-gapped networks)
- `tun`: Interface name, MTU (default 1400), IPv4/IPv6 addresses
- `socks5`: Proxy server, auth credentials (password also via `MUTIAUK_SOCKS5_PASSWORD` env), timeouts, transport (tcp/websocket; auto-detected from `ws://`/`wss://` scheme)
- `routes`: Static CIDR destinations to forward through the proxy. `enabled` defaults to `true` when the YAML omits the key; the daemon emits a per-route WARN at startup so legacy configs surface in journald
- `autoroutes`: Route/host state from a Muti Metroo agent (`source` auto|socks|dashboard; SOCKS route-watch by default, optional HTTP `url`/`token`/`MUTI_METROO_TOKEN` fallback, `poll_interval`); also gates the local DNS resolver
- `dns_integration`: `auto` (default), `resolvectl`, `nm-dnsmasq`, `etc-resolver` (macOS), `nrpt` (Windows), or `none`
- `expose`: Publish local services to the mesh (folders or TCP forwards) over the SOCKS5 endpoint (`socks5.server`) via the `CmdExpose` stream; authorization is the SOCKS5 handshake. Knobs: `reconnect_min`/`reconnect_max` (jittered backoff), `keepalive_period`, `max_streams_per_key` (default 256), `max_buffered_bytes` (default 16 MiB), `forwards`. Forwards reload on SIGHUP; runtime add/remove via `mutiauk expose` CLI / Unix-socket API
- `admin`: Local admin HTTP interface (browser dashboard + JSON endpoints) served by the daemon over `internal/adminhttp`. Enabled by default (`enabled` is a `*bool`: an omitted `admin:` section, or an `admin:` block with no `enabled:` key, defaults to enabled; explicit `enabled: false` disables). Loopback-only and token-authenticated: every endpoint requires the bearer token from the root-only `admin-token` file next to the config, requests must carry a loopback Host header, and POST/DELETE require `Content-Type: application/json`; `Validate` rejects any non-loopback `listen` host. `listen` defaults to `127.0.0.1:48088`, and the daemon best-effort also binds the matching `::1`/`127.0.0.1` of the other family on the same port. The interface is a thin layer over `api.StateProvider` (the daemon satisfies it), so it shares state with the Unix-socket control plane: `GET /api/status|/api/agent|/api/routes|/api/expose`, `POST /api/expose`, `DELETE /api/expose/{key}`. `mutiauk admin` prints a dashboard login URL
- `nat`: Connection table size and timeouts
- `logging`: Level, format, output

Config files are written with mode `0600` by both `cfg.Save` and the wizard since they carry `socks5.password` and `autoroutes.token`. `mutiauk config check` warns when a hand-rolled file is group- or world-readable. Use `Config.ApplyDefaults()` (exported) before `Validate()` when constructing a Config in code; `config.Load()` calls it automatically.

## CLI surface

Top-level subcommands (registered in `internal/cli/root.go`):

- `setup` - Interactive TTY wizard only (requires a TTY). Point it at an existing config (the path it asks for) to edit it: current values become the prompt defaults and sections the wizard never asks about (expose, NAT, dns_integration, daemon, logging) are preserved via a clone of the loaded config. For unattended generation, use `config generate` instead.
- `daemon` - `start`, `stop` (with `--wait` default-true, `--timeout`, `--force` for SIGKILL escalation), `reload`, `status` (lightweight pid/uptime/config_path; suitable for k8s readiness checks).
- `route` - `add`/`remove` (refuse direct kernel mutation when daemon is offline unless `--force`), `list`, `plan`, `apply`, `check`, `trace`.
- `host` - `list` (read-only; mesh hosts are managed centrally on the agent).
- `dns` - `status` (backend, active TLDs, host-side state, live probes).
- `expose` - `status`, `list`, `add folder|tcp`, `remove`.
- `status` - Full system status with connectivity probes. Flags: `--exit-code`, `--quiet` (implies `--exit-code`), `--check tun|socks5|tcp|udp|expose|daemon` (repeatable; scopes the exit code), `--skip-tests`, `--json`.
- `admin` - Print the admin dashboard login URL (carries the token from the 0600 `admin-token` file as `?token=`, which the daemon exchanges for a session cookie).
- `service` - `install`, `uninstall`, `status` (with `--json` returning `{installed, active, enabled, sub_state}`).
- `config` - `check` (validate the file; `--strict`, `--json` for CI gating); `generate` (non-interactive config generation for Ansible/Salt/cloud-init: layers `wizard.BuildBaselineConfig()` defaults < `--from <answers.yaml>` < per-field flags (`--tun-name`, `--socks5-server`, `--autoroutes-url`, ...), validates, writes at mode 0600. Flags: `--from`, `--output`/`-o`, `--force`, `--json`. Secrets come from `MUTI_METROO_TOKEN` / `MUTIAUK_SOCKS5_PASSWORD` env vars; never written to the answers file or command line. Service install is left to `service install`).
- `cleanup` - Reset host state after crash; `--dry-run` previews actions, `--purge` also removes the route state file.
- `completion` - Emit shell-completion for `bash|zsh|fish|powershell`.
- `version` - Print version, git commit, build date.

On Unix the config file and the daemon's API Unix socket are 0600/root-owned, so every CLI subcommand that talks to the daemon needs `sudo`; on Windows the equivalent is the named-pipe control endpoint (restricted to SYSTEM + Administrators) and config under `%ProgramData%`, so commands must run from an elevated prompt. `loadConfig` in `internal/cli/helpers.go` wraps `fs.ErrPermission` with a "rerun with sudo" hint; `rootCmd.SilenceUsage = true` so RunE errors don't dump the flag list.

Output conventions across subcommands: probe markers use the four-state vocabulary in `internal/cli/labels.go` (`OK`, `FAIL`, `WARN`, `SKIP`); JSON encoders go through `emitJSON(w, v)` in `internal/cli/helpers.go`; timestamps use `formatTimestamp(t)` for RFC3339 + relative ago.

## Documentation

Mutiauk documentation is part of the Muti Metroo documentation site:
- **Source:** `../Muti Metroo/docs/docs/mutiauk.mdx`
- **URL:** https://mutimetroo.com/mutiauk/

Edit documentation in the Muti Metroo docs directory.

## Releasing

Releases are driven by release-please (`.github/workflows/release-please.yml`).
Commits on master use Conventional Commits; `fix:`/`feat:` commits make
release-please propose a patch/minor release PR. Merging that PR tags the
release, bumps `CHANGELOG.md`, `VERSION`, and the manifest pin, and the
workflow builds and uploads the Linux amd64/arm64 binaries + checksums.

`release.sh` remains as a manual alternative for ad-hoc full releases (all
six targets, run from a maintainer machine). Do not use it for routine
releases.

### Manual release via release.sh

Prerequisites:

- `GITHUB_TOKEN` environment variable set with a GitHub API token
- `docker`, `jq`, `curl` installed
- Builds run inside `golang:1.25` Docker image (gVisor requires Linux + matching Go toolchain)
- UPX compression is installed automatically inside the build container

```bash
# Patch release (default): 0.5.1 -> 0.5.2
./release.sh

# Minor release: 0.5.1 -> 0.6.0
./release.sh minor

# Major release: 0.5.1 -> 1.0.0
./release.sh major
```

The script will:
1. Bump version in `VERSION` file
2. Cross-compile Linux binaries (amd64 + arm64) inside Docker
3. Compress with UPX
4. Generate SHA256 checksums
5. Commit version bump, create git tag, push
6. Create GitHub release and upload assets

### Manual Testing

Test infrastructure is in the `test/` directory:
- Start environment: `make docker-test`
- Get shell: `make docker-shell`
