# AGENTS.md

Guidance for AI coding agents working in this repository.

## Project overview

Mutiauk is a TUN-interface ingress client for the Muti Metroo mesh VPN. It is a
single Go binary (`cmd/mutiauk`) with two modes: a daemon that creates a virtual
TUN interface and forwards selected TCP/UDP/ICMP traffic through a SOCKS5
endpoint (using the gVisor userspace TCP/IP stack), and a CLI for route
management, service integration, and diagnostics.

Companion projects:

- **Muti Metroo** (`github.com/Muti-Metroo/Muti-Metroo`) - the mesh agent Mutiauk
  connects to. Its SOCKS5 endpoint is Mutiauk's data plane; the agent dashboard
  feeds autoroutes/DNS, and expose streams ride the same endpoint.
- **Muti Metroo Manager** (`github.com/Muti-Metroo/Muti-Metroo-Manager`) - the
  central management UI/API.

Supported platforms: Linux (`/dev/net/tun`, netlink, systemd,
resolvectl/nm-dnsmasq), macOS (utun, `/sbin/route` + PF_ROUTE, launchd,
`/etc/resolver`), Windows (embedded Wintun, iphlpapi via winipcfg, Service
Control Manager, NRPT). TUN creation and route changes require root /
Administrator.

## Repo layout

- `cmd/mutiauk/` - binary entry point
- `internal/cli/` - CLI subcommands (root, daemon, route, host, dns, expose,
  status, service, config, cleanup, completion, setup wizard glue)
- `internal/daemon/` - orchestrator wiring TUN + gVisor stack + routes + API +
  admin HTTP + autoroutes + expose
- `internal/tun/`, `internal/stack/`, `internal/proxy/` - TUN backends, gVisor
  stack wrapper, TCP/UDP/ICMP forwarders
- `internal/socks5/` - SOCKS5 client (TCP CONNECT, UDP STREAM with UDP ASSOCIATE fallback, ICMP echo,
  expose streams; plain TCP and WebSocket transports)
- `internal/route/`, `internal/nat/`, `internal/dns/` - route management,
  connection tracking, mesh DNS resolver + host-DNS integrations
- `internal/autoroutes/` - route/host/TLD state from the Muti Metroo agent
- `internal/expose/` - publish local folders/TCP services to the mesh
- `internal/api/` - Unix-socket / named-pipe control plane
- `internal/adminhttp/` - loopback admin dashboard (embedded, vendored
  Bootstrap; no CDN); all endpoints require the bearer token from the
  0600 `admin-token` file next to the config
- `internal/config/`, `internal/service/`, `internal/wizard/` - config
  load/validate, service install, interactive setup
- `configs/` - example YAML configs; `test/` - Docker test rigs and load tests

Platform-specific code lives in `_linux.go` / `_darwin.go` / `_windows.go` /
`_stub.go` files behind shared interfaces.

## Build and test

**Go is pinned to 1.25.5**: the vendored gVisor dependency does not compile
under Go 1.26 (`pkg/sync` constant redeclaration). If the host's default Go is
newer than 1.25, prefix every go command:

```bash
GOTOOLCHAIN=go1.25.5 go build ./...
GOTOOLCHAIN=go1.25.5 go test ./...        # 17 packages
GOTOOLCHAIN=go1.25.5 go vet ./...
```

The Makefile pins the toolchain for the darwin/windows cross-compile targets
but not for `build`/`test`, so the same prefix applies to `make build` /
`make test` on a Go 1.26+ host:

```bash
make build                 # current platform -> bin/mutiauk
make build-all             # linux/darwin/windows, amd64+arm64
make test                  # go test -v ./...
make lint / make fmt / make tidy
make docker-test           # full Docker test rig (Kali + SOCKS5 + nginx)
make loadtest-up           # load-test environment against a Muti Metroo tunnel
```

Releases in CI run on `ubuntu-latest` with Go 1.25, so Linux-targeted work can
also be verified with `make docker-test` from macOS.

## Release process

Releases are driven by **release-please** (`.github/workflows/release-please.yml`,
config in `.github/release-please-config.json`, manifest pin in
`.github/.release-please-manifest.json`):

- Commits on master must use **Conventional Commits**; `fix:` and `feat:`
  (and breaking `!`) change the proposed release version, `docs:`/`chore:`/
  `test:`/`refactor:` do not. `fix(deps):` counts as `fix:` and triggers a
  patch release proposal.
- release-please opens/updates a release PR; merging it tags the release,
  updates `CHANGELOG.md` and the manifest pin, and the workflow
  builds and uploads Linux amd64/arm64 binaries + checksums to the GitHub
  release.
- Never tag or create releases by hand.

## Branding and writing rules

- Product names: **Mutiauk** (this binary), **Muti Metroo** (the mesh agent),
  **Muti Metroo Manager** (the management UI). Never reintroduce legacy fork
  branding; `git grep -in 'meshy'` must stay empty.
- Module path is `github.com/Muti-Metroo/Mutiauk`; the binary is `mutiauk`.
- Environment variables: `MUTIAUK_SOCKS5_PASSWORD` (this daemon's SOCKS5
  password). There is no agent API token; autoroutes and expose authorize
  through the SOCKS5 handshake.
- **English-only, ASCII-only** in code, comments, docs, and commit messages
  (no emoji, no non-ASCII prose; em-dashes and friends included).
- User-facing probe markers use the four-state vocabulary in
  `internal/cli/labels.go` (`OK`, `FAIL`, `WARN`, `SKIP`).
