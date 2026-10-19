# Mutiauk

TUN interface for the operator workstation in the [Muti Metroo](https://mutimetroo.com/) suite, for Linux, macOS, and Windows.

Mutiauk creates a virtual network interface (TUN) and forwards selected TCP, UDP,
and ICMP echo traffic into the SOCKS5 listener of a Muti Metroo ingress agent,
using Google's gVisor userspace TCP/IP stack.

In a Muti Metroo deployment, `muti-metroo` agents run on the mesh nodes (relays,
gateways, transit hops, and exits inside target networks). The ingress agent, the
one with the SOCKS5 listener enabled, is usually a mesh node too, such as a public
relay. The operator's workstation runs Mutiauk, which is the usual entry point into
the mesh: with `autoroutes.enabled: true` it receives the mesh's CIDR routes, mesh
hostnames, and DNS suffixes from the agent over that same SOCKS5 endpoint, so
ordinary tools (ssh, curl, browsers, nmap, database clients) reach mesh networks
with no proxy settings. The operator typically also runs
[Muti Metroo Manager](https://github.com/Muti-Metroo/Muti-Metroo-Manager) for the
topology view and management actions; its Mutiauk wizard can generate this config.

Pointing SOCKS-aware applications directly at the agent's SOCKS5 listener remains
an option for quick tests, single applications, machines without root or
Administrator, and domain-based exit routes (Mutiauk only sees IP addresses, so it
uses CIDR routes only).

Documentation: https://mutimetroo.com/mutiauk

## Features

- Route specific CIDRs through SOCKS5 proxy
- TCP and UDP forwarding support
- WebSocket transport for firewall bypass
- Hot reload configuration via SIGHUP
- Userspace networking (no kernel modifications)
- Automatic routes: CIDR routes pushed by the Muti Metroo agent over the SOCKS5 endpoint (no separate URL or token)
- Mesh hostname resolution via in-process DNS server; the mesh-host registry and active TLD suffixes received from the agent over the SOCKS5 endpoint
- Pluggable host-DNS integration: `resolvectl` (systemd-resolved) or NetworkManager's dnsmasq plugin on Linux, `/etc/resolver` on macOS, NRPT on Windows; configurable via `dns_integration`
- `dns status` diagnostic command for one-shot inspection of backend, active TLDs, host-side state, and live probes
- `route trace` resolves mesh hostnames and reports the resolution source (mesh / system / literal IP)
- Publish local services to the mesh: serve a folder over HTTP or forward a TCP target via the `expose` subsystem (a persistent multiplexed stream to the upstream Muti Metroo agent over the SOCKS5 endpoint, no inbound port). Per-key concurrent-stream cap and per-stream buffer cap protect against runaway peers.
- Non-interactive config generation for Ansible/Salt/cloud-init: `mutiauk config generate --from answers.yaml --json` writes `/etc/mutiauk/config.yaml` (mode 0600), validates, and returns a structured report. (Register the systemd unit separately with `mutiauk service install`.)
- Lightweight `mutiauk daemon status` (pid/uptime; no network probes) for k8s readiness checks; `service status --json` for fleet tooling.
- Per-component health checks: `mutiauk status --check socks5 --check tcp --exit-code` returns non-zero only when the listed components fail. `--quiet` for monitoring scripts.
- Graceful shutdown: `mutiauk daemon stop --wait` blocks until the process exits, with `--timeout` and `--force` for SIGKILL escalation.
- `mutiauk config check` validates a config file (with `--strict` and `--json` for CI gating). Warns on world-/group-readable files, defaulted `enabled:` keys, and legacy `dns:` blocks.
- Shell completion: `mutiauk completion bash|zsh|fish|powershell`.
- Local control API for CLI-daemon communication (Unix socket; named pipe on Windows)
- Route persistence with `--persist` flag

## Requirements

- Linux with TUN support (`/dev/net/tun`), macOS (utun), or Windows (the Wintun driver is embedded in the binary)
- Root privileges (CAP_NET_ADMIN) on Linux and macOS, Administrator on Windows
- A Muti Metroo agent with the SOCKS5 listener enabled (any SOCKS5 server works for plain traffic forwarding; autoroutes, mesh DNS, and expose need a Muti Metroo agent)

### Connecting to a remote agent

When the ingress agent runs on another host, enable SOCKS5 authentication on the agent. Plain SOCKS5 over TCP is not encrypted between the workstation and the agent, credentials included, so prefer the agent's SOCKS5 WebSocket listener (`socks5.websocket`) over TLS and set `socks5.server: wss://relay.example.com:8443/socks5`, or reach the agent over a network you already trust. Mutiauk verifies the server certificate against the system trust store, so the agent needs a certificate the workstation trusts (a self-signed agent certificate is rejected).

## Installation

Download the latest Linux release from the
[Releases](https://github.com/Muti-Metroo/Mutiauk/releases) page.

```bash
# Download
curl -L -o mutiauk https://github.com/Muti-Metroo/Mutiauk/releases/latest/download/mutiauk-linux-amd64
chmod +x mutiauk
sudo mv mutiauk /usr/local/bin/
```

Releases include Linux binaries only. Build macOS and Windows binaries from source (see [Building](#building)).

## Usage

New here? Run the interactive setup wizard:

```bash
sudo mutiauk setup
```

The wizard asks for the SOCKS5 server as `host:port` (plain TCP). For a `wss://` server, use `config generate --socks5-server wss://...`, the Mutiauk wizard in Muti Metroo Manager, or edit the config file.

The wizard can also edit an existing config: give it the path to a current `config.yaml` and its values become the prompt defaults, with sections it never asks about (expose, NAT, DNS, logging) preserved.

For Ansible/Salt/cloud-init, generate the config non-interactively with `config generate`. The answers file is just a partial (or full) `config.yaml` (same schema, missing fields fall to defaults). Per-field flags override the answers file (Helm-style); secrets come from the `MUTIAUK_SOCKS5_PASSWORD` env var rather than the file or command line.

```bash
cat <<EOF > /tmp/answers.yaml
tun: { name: tun0, address: 10.7.0.1/24 }
socks5: { server: "wss://relay.example.com:8443/socks5", username: ops }
autoroutes: { enabled: true }
EOF

MUTIAUK_SOCKS5_PASSWORD=secret \
  sudo mutiauk config generate --from /tmp/answers.yaml -o /etc/mutiauk/config.yaml

# Then register the service:
sudo mutiauk service install -c /etc/mutiauk/config.yaml
```

Flags: `--from`, `--output`/`-o`, `--force`, `--json`, and per-field overrides (`--tun-name`, `--socks5-server`, `--autoroutes-enabled`, ...).

Otherwise, the common commands (the config file and the daemon's API Unix socket are both `0600`/root, so all of these except shell-completion need `sudo`; on Windows, run them from an elevated prompt):

```bash
# Daemon lifecycle
sudo mutiauk daemon start                     # config defaults to /etc/mutiauk/config.yaml
sudo mutiauk daemon stop                      # blocks until exit; --timeout 30s, --force for SIGKILL
sudo mutiauk daemon reload                    # SIGHUP
sudo mutiauk daemon status                    # lightweight pid/uptime (suitable for k8s readiness)
sudo mutiauk daemon status --json

# Full system status (includes connectivity probes)
sudo mutiauk status
sudo mutiauk status --exit-code               # non-zero if unhealthy
sudo mutiauk status --check socks5 --check tcp --exit-code  # scope to specific components
sudo mutiauk status --quiet --exit-code       # silent; for monitoring scripts
sudo mutiauk status --json

# Config validation (CI gating)
sudo mutiauk config check
sudo mutiauk config check --strict --json     # treat warnings as errors

# Manage routes
sudo mutiauk route list
sudo mutiauk route add 10.0.0.0/8             # ephemeral (lost on daemon restart)
sudo mutiauk route add 10.0.0.0/8 --persist   # save to config file
sudo mutiauk route remove 10.0.0.0/8
sudo mutiauk route trace 10.10.5.100          # show match + mesh path
sudo mutiauk route trace internal.corp.local --json

# Mesh DNS + host registry diagnostics
sudo mutiauk dns status
sudo mutiauk dns status --json
sudo mutiauk host list

# Publish local services to the mesh (over the SOCKS5 endpoint; set expose.enabled)
sudo mutiauk expose status
sudo mutiauk expose add folder my-docs /srv/docs
sudo mutiauk expose add tcp my-ssh 127.0.0.1:22
sudo mutiauk expose remove my-docs

# Service management
sudo mutiauk service status                   # human output
sudo mutiauk service status --json            # {installed, active, enabled, sub_state}
sudo mutiauk service install
sudo mutiauk service uninstall

# Recovery + completion
sudo mutiauk cleanup --dry-run                # preview without touching anything
sudo mutiauk cleanup                          # remove TUN, PID, socket; revert host DNS
mutiauk completion bash | sudo tee /etc/bash_completion.d/mutiauk
```

## Configuration

See [configs/mutiauk.example.yaml](configs/mutiauk.example.yaml) for a complete
example configuration.

```yaml
tun:
  name: tun0
  mtu: 1500
  address: 10.200.200.1/24

socks5:
  # SOCKS5 listener of the Muti Metroo ingress agent.
  # wss:// selects the SOCKS5 WebSocket listener over TLS (recommended for a remote agent).
  server: wss://relay.example.com:8443/socks5
  # Or plain TCP (unencrypted between this host and the agent):
  # server: relay.example.com:1080
  # Or explicit transport with host:port (TLS):
  # transport: websocket  # "tcp" (default) or "websocket"
  # ws_path: /socks5
  # username/password for SOCKS5 auth and WebSocket HTTP Basic Auth
  username: operator
  # password: secret      # or set MUTIAUK_SOCKS5_PASSWORD

routes:
  - destination: 192.168.0.0/16
    enabled: true
  - destination: 10.0.0.0/8
    enabled: true

# Automatic routes + mesh DNS from the Muti Metroo agent
autoroutes:
  # Receive routes/hosts/DNS suffixes over the SOCKS5 endpoint (socks5.server).
  # No separate URL or token. Off by default.
  enabled: true
  # stale_timeout: 10m  # optional: flush auto routes + mesh DNS when no snapshot arrives
```

Route, mesh-host, and suffix updates are pushed over the same SOCKS5 endpoint used for traffic (`socks5.server`) the moment they change, so a typical deploy only needs that one endpoint. Authorization is the SOCKS5 handshake.

When `autoroutes.enabled` is true, Mutiauk also starts a local DNS resolver bound to the TUN IPv4 address on port 53. It receives the mesh-host registry and active TLD suffixes (e.g. `.mesh`) from the agent over the SOCKS5 endpoint and wires them into the host resolver via whichever integration is available (systemd-resolved `resolvectl` or NetworkManager's dnsmasq plugin on Linux, `/etc/resolver` on macOS, NRPT on Windows), so queries for those suffixes are routed through the TUN. Routes and the suffix set are applied atomically on each update. See [Mesh DNS on Kali](#mesh-dns-on-kali) below.

### Secrets

A secret can be supplied via the environment instead of YAML, so deployments using systemd `LoadCredential=` or k8s secret-mounted env vars never need to write the secret to `/etc/mutiauk/config.yaml`:

| Env var | Overrides | Notes |
| --- | --- | --- |
| `MUTIAUK_SOCKS5_PASSWORD` | `socks5.password` | Used together with `socks5.username`. |

Env wins over YAML. The config file itself is written with mode `0600` by both `cfg.Save` and the wizard; `mutiauk config check` warns when a hand-rolled config is group- or world-readable.

## Expose

The `expose` block lets the operator publish local services to the mesh without standing up a full Muti Metroo agent. Mutiauk opens a single `CmdExpose` stream over the SOCKS5 endpoint (`socks5.server`) to the upstream agent and registers each forward by routing key. When the agent receives a forward stream for that key, it pipes the connection back over the expose stream and Mutiauk handles it locally:

- `type: folder`: strict static file server. Returns an empty 404 (Apache-style body, no `Server` header) for any path that does not exactly resolve to a regular file under `path`. No directory listings, no `index.html` fallback, no welcome page.
- `type: tcp`: dial `target` and pipe bytes bidirectionally.

Forwards declared in YAML are loaded at startup and reload on `SIGHUP`. `mutiauk expose add|remove` mutates them at runtime (lost when the daemon restarts; runtime entries cannot collide with config-defined keys). Multiple concurrent inbound connections per key are multiplexed over the same expose stream.

```yaml
expose:
  enabled: true
  forwards:
    - key: op-docs
      type: folder
      path: /srv/docs
      comment: "operator runbook"
    - key: op-ssh
      type: tcp
      target: 127.0.0.1:22
```

Requirements:

- `socks5.server` must point at the upstream Muti Metroo agent. The expose stream rides the same SOCKS5 endpoint; authorization is the SOCKS5 handshake.
- Folder paths must be absolute and exist at startup.
- Routing keys must match `[a-zA-Z0-9._-]+`.

DoS guards (configurable; defaults shown):

- `max_streams_per_key: 256`: the (Max+1)-th concurrent OPEN for a single routing key is refused with an `OPEN_ACK` error so a runaway peer cannot exhaust file descriptors. Existing streams keep running.
- `max_buffered_bytes: 16777216` (16 MiB): per-stream receive buffer cap. The protocol has no flow control, so this is the only backpressure: hitting the cap closes the stream cleanly. Lower on resource-constrained hosts; minimum 64 KiB.

Runtime control via the daemon's Unix socket:

```bash
mutiauk expose status
mutiauk expose add folder shared-docs /srv/share --comment "team share"
mutiauk expose add tcp local-ssh 127.0.0.1:22
mutiauk expose remove shared-docs
```

## Mesh DNS on Kali

When `autoroutes.enabled` is true, Mutiauk runs a local DNS resolver on the TUN address and auto-detects the right host integration to forward mesh-suffix queries through it. Two backends are supported:

- **`resolvectl` (systemd-resolved)**: preferred. Per-link DNS config; suffix changes propagate live.
- **`nm-dnsmasq`**: NetworkManager's built-in dnsmasq plugin. Used when systemd-resolved isn't available. Mutiauk writes `/etc/NetworkManager/dnsmasq.d/mutiauk.conf` and reloads NM whenever the TLD set changes.

If neither is present, the daemon logs a one-line warning and stays out of the way; you can still query the resolver directly at the TUN address.

Selection is automatic. Override with `dns_integration: auto|resolvectl|nm-dnsmasq|none` in the config if you want to force a specific path. Use `mutiauk dns status` to see what's active.

### Path A: systemd-resolved (recommended for typical Kali)

Kali ships without systemd-resolved by default; install it once:

```bash
sudo apt update && sudo apt install -y systemd-resolved
sudo tee /etc/NetworkManager/conf.d/dns.conf >/dev/null <<'EOF'
[main]
dns=systemd-resolved
EOF
sudo ln -sf /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf
sudo systemctl enable --now systemd-resolved
sudo systemctl restart NetworkManager
sudo systemctl restart mutiauk
```

If systemd-resolved was installed after dbus was already running, dbus may cache the resolved bus name as `(activatable)` and then time out trying to start it on demand. Symptom in the daemon log: `Failed to activate service 'org.freedesktop.resolve1': timed out`. Fix:

```bash
sudo systemctl reload dbus
sudo systemctl restart systemd-resolved
sudo systemctl restart mutiauk
```

### Path B: NetworkManager dnsmasq plugin (when resolved isn't an option)

Switch NM to the dnsmasq plugin once; Mutiauk will detect this on the next start and manage the per-suffix drop-in for you:

```bash
sudo tee /etc/NetworkManager/conf.d/dns.conf >/dev/null <<'EOF'
[main]
dns=dnsmasq
EOF
sudo systemctl restart NetworkManager
sudo systemctl restart mutiauk
```

The daemon writes `/etc/NetworkManager/dnsmasq.d/mutiauk.conf` with one `server=/<suffix>/<resolver-ip>` line per active TLD and reloads NM. Suffix changes on the agent propagate automatically; the file is rewritten and NM reloaded only when the TLD set actually changes.

### Verify

```bash
sudo mutiauk dns status
```

Sample (resolvectl backend):

```
Backend:    resolvectl (auto-detected)
Listen:     10.200.200.1:53
Active TLDs: .mesh .mesh.alt    applied 2026-05-05T14:22:01Z (18s ago)
Hosts:      2 registered

Host state (resolvectl):
  DNS Servers: 10.200.200.1
  DNS Domain:  ~mesh ~mesh.alt

Probes:
  [OK] resolver direct -- 10.200.200.1:53 -> NXDOMAIN
  [OK] system stub -- 127.0.0.53:53 -> NXDOMAIN
```

The `OK` markers on NXDOMAIN look counterintuitive but are correct: the probes use a sentinel name that's deliberately not registered. NXDOMAIN proves the resolver is reachable and answering; SERVFAIL or timeout would mean the wiring is broken.

Then a real lookup:

```bash
dig +short host.mesh
```

### Notes

- **Bare single-label TLDs** (e.g. `dig mesh` for a host registered as the bare `mesh`) are not forwarded by systemd-resolved by default. The resolver itself answers them correctly when queried directly at the TUN address. To forward them through the stub, set `ResolveUnicastSingleLabel=yes` in `/etc/systemd/resolved.conf` and restart resolved.
- **`mutiauk cleanup`** reverts both backends (best-effort; idempotent), so you can switch between paths without leaving stale state behind.
- **Disabling the host integration entirely** (e.g. when you want to wire DNS yourself) is `dns_integration: none` in the config. The resolver still runs on the TUN address; only the host-side forwarding rule is skipped.

## Documentation

Full documentation: https://mutimetroo.com/mutiauk

## Benchmarks

Performance benchmarks running through a Muti Metroo SOCKS5 tunnel:

| Protocol | Throughput | Duration | Notes |
|----------|------------|----------|-------|
| TCP | 1.42 Gbits/sec | 60s | 10 concurrent streams via iperf3 |
| UDP | 100 Mbits/sec | 60s | 0.09% packet loss, 0.2ms jitter |

**Test environment:** All components (TUN client, SOCKS5 ingress, exit node, echo
server) running on the same machine in Docker containers. Real-world performance
over network links will vary based on latency and bandwidth.

**Data path:**
```
Client -> TUN -> Mutiauk -> SOCKS5 -> Muti Metroo (QUIC) -> Exit -> Target
```

See [test/loadtest/](test/loadtest/) for the benchmark code and instructions.

## Building

```bash
# Build for Linux
make build-linux

# Build for ARM64
make build-linux-arm64

# Build for macOS and Windows (these targets pin GOTOOLCHAIN=go1.25.5;
# the gVisor dependency does not compile with Go 1.26)
make build-darwin           # bin/mutiauk-darwin-arm64
make build-darwin-amd64     # bin/mutiauk-darwin-amd64
make build-windows          # bin/mutiauk-windows-amd64.exe (embeds Wintun)
make build-windows-arm64    # bin/mutiauk-windows-arm64.exe
make build-all              # every supported target

# Run tests
make test

# Run load tests
make loadtest-up      # Start test environment
make loadtest-shell   # Get shell in test container
make loadtest-run     # Run full test suite
make loadtest-down    # Stop test environment
```

## License

MIT License - see [LICENSE](LICENSE) for details.
