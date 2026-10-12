# Windows Support + SCM Service Runner: Implementation Plan

Status: **IMPLEMENTED** and verified end-to-end on Windows 11 ARM64 (elevated)
with cross-platform unit tests passing. The feasibility was first proven by
cross-compiling the full tree (gVisor's `pkg/tcpip` builds clean for
`GOOS=windows`), then built out behind the existing platform seams that the
macOS port established. ASCII only, per repo convention.

Implementation checklist (all done):
- [x] TUN device - `tun/device_windows.go` (Wintun, embedded + SHA-256-verified DLL)
- [x] Link endpoint - `stack/linkendpoint_windows.go` (gVisor channel + pump; raw IP, no AF header)
- [x] Routes - `route/{kernel,lookup}_windows.go` (iphlpapi via winipcfg + GetBestRoute2)
- [x] DNS integration - `dns/nrpt_windows.go` (NRPT registry rules) + `dns/nrpt_other.go` noop
- [x] Service runner - `service/service_windows.go` (Service Control Manager)
- [x] Daemon lifecycle - `cli/daemon_run_windows.go` (svc.Run; SCM stop -> graceful drain)
- [x] Control plane - named-pipe IPC + `daemon.reload`/`daemon.stop`/`daemon.ping` API methods
- [x] Per-OS paths - `config/paths_windows.go`, `route/paths_windows.go` (%ProgramData%, named pipe)
- [x] Build - `make build-windows` / `build-windows-arm64`; built from source (the release workflow ships Linux only)

Autoroutes and expose required no Windows-specific code: they ride on the
route, DNS, and websocket layers, all of which are platform-neutral.

## Architecture: API as the cross-platform control plane

Windows has no Unix signals (SIGHUP/SIGTERM/SIGKILL). Rather than a
Windows-only control path, the API socket was promoted to the primary
control plane on **all** platforms: `daemon reload` and `daemon stop`
prefer the control endpoint (the new `daemon.reload`/`daemon.stop`/
`daemon.ping` methods), and fall back to Unix signals only on Linux/macOS
when the socket is unreachable (so `systemd ExecReload=kill -HUP` still
works). `daemon.stop` flushes its response before cancelling the context
(via `time.AfterFunc`) so the client reads `{success:true}` rather than a
reset.

The IPC transport is build-tagged (`api/transport_{unix,windows}.go`): a
Unix-domain socket at mode 0600 on Linux/macOS, and a named pipe
(`\\.\pipe\mutiauk`) with an `SYSTEM`+`Administrators` security
descriptor on Windows -- the real 0600 equivalent (AF_UNIX's `chmod` is a
no-op on Windows, which would silently lose the access guarantee).

## Component design

### TUN device (`tun/device_windows.go`)
Built on the low-level `golang.zx2c4.com/wintun` session API
(`CreateAdapter` -> `StartSession` -> `ReceivePacket`/`SendPacket`).
Wintun delivers raw IP packets (no BSD address-family header, unlike
macOS utun), so Read/Write copy straight through. Addresses and MTU are
set via `winipcfg.LUID`; IPv6 failures are tolerated so IPv4-only hosts
work. Shutdown follows the WireGuard pattern: a close flag plus a
WaitGroup so `Close` drains in-flight Read/Write before ending the
session and deleting the adapter.

The signed Wintun 0.14.1 driver DLL is embedded per arch via `go:embed`
(`dll_{amd64,arm64}_windows.go`), extracted to `%ProgramData%\Muti Metroo
Entry\wintun.dll`, **SHA-256-verified before loading**, and pre-loaded by
absolute path (the wintun package's later by-name load then resolves to
our verified copy, since Windows matches loaded modules by base name).
The .exe is therefore self-contained.

### Link endpoint (`stack/linkendpoint_windows.go`)
A `windowsPump` mirroring the macOS `darwinPump`: a gVisor channel
endpoint plus inbound/outbound goroutines. Simpler than darwin since
there is no AF header. `protocolNumber`/`channelQueueLen` are duplicated
from the darwin file (8 lines) to avoid cross-build-tag coupling.

### Routes + lookup (`route/{kernel,lookup}_windows.go`)
`KernelRoutes` uses winipcfg's `*2` IP Helper wrappers
(`AddRoute`/`DeleteRoute`/`GetIPForwardTable2`), keyed on the adapter
**LUID** (stable across ifIndex changes). Add/Remove are idempotent like
the darwin path. `Lookup` binds `iphlpapi!GetBestRoute2` directly (not
exposed by winipcfg) for the authoritative best-route answer.

### DNS integration (`dns/nrpt_windows.go`)
A new `IntegrationNRPT` backend writes one NRPT (Name Resolution Policy
Table) rule per active mesh TLD directly to the registry under
`...\Dnscache\Parameters\DnsPolicyConfig\<GUID>`, forwarding that
namespace to the local resolver. Rules are reconciled to the exact suffix
set on each Apply, identified by a deterministic per-suffix GUID plus a
management marker so only our own rules are ever modified or removed
(mirroring the macOS `/etc/resolver` reconcile). `DnsFlushResolverCache`
is invoked after changes. `DetectIntegration` auto-selects NRPT on
Windows.

### Service runner (`service/service_windows.go`)
Install/uninstall/status via the Service Control Manager
(`x/sys/windows/svc/mgr`), auto-start, running as LocalSystem (which can
create the adapter and edit routes/NRPT). When launched by the SCM,
`cli/daemon_run_windows.go` detects the service environment
(`svc.IsWindowsService`) and runs under `svc.Run`, mapping SCM
Stop/Shutdown to graceful context cancellation and **waiting for the run
goroutine to drain** (with a generous `WaitHint`) so the deferred
teardown -- PID file, adapter, routes, NRPT -- completes before the
service reports Stopped.

### Liveness + force-kill (`cli/proc_windows.go`)
`os.FindProcess` always "succeeds" on Windows, so `pidAlive` queries the
process exit code (`OpenProcess` + `GetExitCodeProcess`, STILL_ACTIVE) and
`forceKill` uses `TerminateProcess`. The authoritative liveness check is
`daemon.ping` over the pipe.

## Platform seams touched

New `//go:build windows` files implement each seam; the existing
`!linux && !darwin` stub/other files already matched Windows and continue
to supply the no-op resolvectl/nm-dnsmasq/etc-resolver backends, so only
NRPT was added (`nrpt_{windows,other}.go`). The `tun`/`stack`/`route`
`_stub` tags were narrowed to `!linux && !darwin && !windows`.

## Reliability properties (verified on hardware)

- DLL is SHA-256-verified before load; a tampered/short copy is refused.
- A stale adapter from a prior crash is reclaimed (create-first, then
  open+delete+recreate) before a fresh adapter is created.
- Graceful stop (pipe or SCM) tears down the adapter, routes, and PID
  file; `cleanup` reclaims leftovers idempotently.
- IPv6-disabled hosts run IPv4-only without error.

## Build and release

`make build-windows` / `build-windows-arm64` (GOTOOLCHAIN=go1.25.5, same
gVisor constraint as macOS). The release-please workflow ships Linux only,
so Windows binaries are built from source. Leave them **uncompressed**
(UPX-packed Windows binaries are routinely flagged by antivirus). `make deploy-windows-test` scps the arm64 binary to
the test host.

## Windows route-management fix (found during data-flow testing)

On Windows the route manager's declarative `Sync` must NOT remove the
routes Windows auto-creates for the interface. The original Linux/macOS
`List()` returned every route on the interface, so `Sync` deleted the
TUN's own local `/32`, its on-link subnet, and the multicast/broadcast/
link-local routes. Consequences observed on hardware:

- Removing the local `/32` (e.g. `10.200.200.1/32`) made Windows drop
  every inbound packet to the TUN address with `DropReason "Not locally
  destined"` -- so gVisor's TCP SYN-ACK never reached the client and no
  flow could complete.
- Removing the on-link subnet broke source-address selection / neighbor
  resolution for traffic out the interface.

Fix: `route/kernel_windows.go` `List()` filters out system routes
(`isSystemRoute`: the interface's own host/subnet routes plus multicast,
broadcast, link-local, loopback, unspecified) so `Sync` only ever manages
user routes. This matches how WireGuard leaves the OS routes intact.
Regression test: `TestIsSystemRoute`.

## Verification (performed on Windows 11 ARM64)

Cross-compiled on macOS, scp'd to an elevated Windows 11 ARM64 host, with a
launchd Muti Metroo agent (SOCKS5, no-auth, internet egress) at `10.8.0.2:1080`
as the upstream proxy.

Lifecycle/management: `version`, `config check`, `daemon start` (DLL
extract + driver install + adapter `mutiauk0` Up at 10.200.200.1 + user
routes installed via iphlpapi), `daemon status`/`reload`/`stop` over the
named pipe (graceful teardown removes adapter + routes), `service
install`/`status --json` (Running, Automatic, LocalSystem; adapter Up
under the service) / `uninstall` (SCM stop drains cleanly), `cleanup`
(idempotent).

Data flows (1.1.1.1/32 + 8.8.8.8/32 routed through the tunnel):
- **TCP: works** -- `curl http://1.1.1.1` returns HTTP 301 in ~0.5s
  through the tunnel.
- **UDP: works** -- DNS queries to 1.1.1.1 and 8.8.8.8 resolve through
  the tunnel (verified with `Resolve-DnsName -Server`).
- **ICMP: partial** -- the ICMP echo request is correctly routed into the
  TUN and reaches gVisor (confirmed by packet capture), and a reply would
  be locally deliverable; but `ping` gets no answer because the SOCKS5
  ICMP relay (Muti Metroo's custom command 0x04) did not respond on this agent.
  This is an agent-side dependency, not a Windows networking defect.

Note (pre-existing, cross-platform, out of scope): `cli.GetLogger` uses
`zap.NewProduction()` and ignores `logging.level`/`format` from config, so
`level: debug` has no effect on any platform.
