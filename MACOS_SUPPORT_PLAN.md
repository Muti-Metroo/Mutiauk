# macOS Support + launchd Service Runner: Implementation Plan

Status: **IMPLEMENTED** and verified on macOS (Darwin 25.x, arm64) with
root-gated tests passing. Feasibility was first proven empirically, then built
out behind the existing platform seams. ASCII only, per repo convention.

Implementation checklist (all done):
- [x] TUN device - `tun/device_darwin.go` (utun + 4-byte AF framing)
- [x] Link endpoint - `stack/linkendpoint_{linux,darwin,stub}.go` (channel + pump)
- [x] Daemon wiring - `server.go` uses `s.ifaceName()` for the kernel utun name
- [x] Routes - `route/{kernel,lookup}_darwin.go` (route CLI + x/net/route + route get)
- [x] Service runner - `service/service_darwin.go` (launchd)
- [x] DNS integration - `dns/resolver_darwin.go` (/etc/resolver) + `hoststate_darwin.go`
- [x] CLI test fix - `shortSocketPath` helper (macOS sun_path limit)
- [x] Build - `make build-darwin` / `build-darwin-amd64` (GOTOOLCHAIN=go1.25.5)

Autoroutes and expose required no darwin-specific code (they ride on the route
and DNS layers above). The sections below are the original plan, kept for
rationale.

## 1. Feasibility summary (what was tested, not guessed)

A throwaway spike (`cmd/macspike`, since deleted) ran as root on this Mac and
exercised the entire data path. Results:

| Concern | Result |
| --- | --- |
| gVisor core stack builds for `darwin/arm64` | PASS - `stack`, `network/ipv4|ipv6`, `transport/tcp|udp`, `adapters/gonet`, `link/channel` have zero linux-only build tags |
| gVisor `link/fdbased` builds for darwin | FAIL (expected) - `endpoint.go`, `packet_dispatchers.go`, `processors.go`, `mmap.go`, and dep `pkg/rawfile` are all `//go:build linux` |
| utun device via `AF_SYSTEM`/`SYSPROTO_CONTROL` | PASS - kernel assigned `utun21`; `x/sys/unix` has `SockaddrCtl`, `CtlInfo`, `IoctlCtlInfo`, `Connect`, `GetsockoptString` |
| 4-byte BSD AF header framing (strip on read, prepend on write) | PASS - real HTTP bytes flowed both directions |
| `channel.Endpoint` + manual fd pump on darwin | PASS - TCP forwarder intercepted with correct src/dst |
| Route via `route add -interface` + `route -n get` | PASS - resolved to `utun21` |
| End-to-end `app -> utun -> gVisor -> TCP forwarder -> reply` | PASS - `200 OK` returned |
| launchd lifecycle (`bootstrap`/`kickstart`/`print`/`bootout`) | PASS in `gui/$UID` domain; `print` exposes `state`, `runs`, `last exit code` mapping cleanly to `service.StatusInfo` |

**Verdict:** gVisor works on macOS. The only hard blocker is the `fdbased` link
endpoint, replaced by `channel.Endpoint` + a two-goroutine packet pump. The
rest is OS-integration glue, each already behind an existing platform interface.

### Hard constraint discovered: Go toolchain

The vendored gVisor (`v0.0.0-20260106...`) does **not** compile under Go 1.26
(`pkg/sync` redeclares `WaitReason*` because both `runtime_constants_go125.go`
and `_go126.go` compile). All darwin builds in this plan used
`GOTOOLCHAIN=go1.25.5`. This matches the existing release flow (the
`golang:1.25` Docker image). No change needed beyond keeping darwin builds on
the 1.25.x toolchain until gVisor is bumped.

## 2. Architecture: what changes and what does not

Unchanged (platform-independent, already proven to run via the channel path):
`socks5/`, `nat/`, `proxy/*` (TCP/UDP/ICMP forwarders), `expose/`,
`autoroutes/`, `config/`, `api/`, `dns/server.go` (the resolver itself),
`stack/intercept_endpoint.go` (pure packet-byte parsing - it intercepts on
`DeliverNetworkPacket`, which `channel.InjectInbound` drives just like
`fdbased`).

New per-platform implementations behind existing seams:

| Layer | Linux (today) | macOS (new) |
| --- | --- | --- |
| TUN device | `tun/device_linux.go` (`/dev/net/tun`, netlink) | `tun/device_darwin.go` (utun, ioctl/route-socket) |
| Link endpoint | `fdbased.New(dev.File())` in `stack/stack.go` | `channel.New` + pump in new `stack/linkendpoint_darwin.go` |
| Kernel routes | `route/kernel_linux.go` (netlink) | `route/kernel_darwin.go` (PF_ROUTE socket) |
| Route lookup | `route/lookup_linux.go` (netlink) | `route/lookup_darwin.go` (route socket / `x/net/route`) |
| DNS host integration | `dns/resolvectl_linux.go`, `dns/nmdnsmasq_linux.go` | `dns/resolver_darwin.go` (`/etc/resolver/<tld>`) |
| DNS host-state diag | `dns/hoststate_linux.go` | `dns/hoststate_darwin.go` |
| Service runner | `service/service_linux.go` (systemd) | `service/service_darwin.go` (launchd) |

## 3. Component-by-component implementation

### 3.1 TUN device - `internal/tun/device_darwin.go` (new)

Change `device_stub.go` build tag `//go:build !linux` -> `//go:build !linux && !darwin`.
Add `device_darwin.go` (`//go:build darwin`) implementing `tun.Device`.

Proven open sequence (constants `SYSPROTO_CONTROL=2`, `UTUN_OPT_IFNAME=2` are
not in `x/sys`; define locally):

```
socket(AF_SYSTEM, SOCK_DGRAM, SYSPROTO_CONTROL)
IoctlCtlInfo(fd, &CtlInfo{Name: "com.apple.net.utun_control"})
Connect(fd, &SockaddrCtl{ID: ci.Id, Unit: <unit>})   // Unit 0 = kernel auto-assign
GetsockoptString(fd, SYSPROTO_CONTROL, UTUN_OPT_IFNAME)  // -> "utunN"
```

Key decisions:
- **AF framing lives in the Device**, not the stack. `Read` strips the leading
  4-byte big-endian address family; `Write` prepends it (AF_INET / AF_INET6
  chosen from the IP version nibble). This keeps `Device` a pure-L3 byte stream
  identical to Linux, so `stack.HandleUDPPacket`/`HandleICMPPacket` (which call
  `tunDev.Write(responsePkt)` directly) work unmodified.
- **Name is advisory.** The kernel forces `utunN`. If `cfg.Name` matches
  `utun\d+`, request `Unit = N+1`; otherwise auto-assign. `Name()` MUST return
  the kernel-assigned name.
- **`File() int`** still returns the fd but becomes Linux-only in practice (the
  darwin stack never wraps it with `fdbased`). Keep it for interface
  compatibility.
- **Address / MTU / up:** use `SIOCAIFADDR` (struct `ifaliasreq`), `SIOCSIFMTU`,
  `SIOCSIFFLAGS` ioctls on an `AF_INET` socket. (Spike used `/sbin/ifconfig`,
  which also works and is an acceptable first cut; ioctls avoid the dependency.)
  utun is point-to-point: set local == dest address for the /32, then rely on
  the route layer for the CIDRs.
- **IPv6:** `SIOCAIFADDR_IN6`; the pump already routes AF_INET6.
- **Close:** `unix.Close(fd)` tears the interface down (kernel auto-removes utun
  on last fd close - verified: no leftover `utun21` after the spike exited).

### 3.2 Link endpoint - `internal/stack/`

`stack/stack.go` currently hard-codes `fdbased.New(tunDev.File())` (line ~134).
Extract that into a platform-split constructor:

- `stack/linkendpoint_linux.go` (`//go:build linux`):
  `newBaseLinkEndpoint(dev, mtu) (stack.LinkEndpoint, pump, error)` ->
  `fdbased.New(...)`, no pump (fdbased owns its own read loop).
- `stack/linkendpoint_darwin.go` (`//go:build darwin`): returns
  `channel.New(queueLen, mtu, "")` plus a `pump` object that, when started:
  - **inbound goroutine:** `unix.Read`-free; calls `dev.Read(buf)` (Device
    already stripped AF) -> `channel.InjectInbound(proto, pkt)`. Proto chosen
    from IP version.
  - **outbound goroutine:** `pkt := ep.ReadContext(ctx)` -> flatten
    `pkt.AsSlices()` -> `dev.Write(flat)` (Device prepends AF).

The existing `interceptEndpoint` continues to wrap the base endpoint for
**inbound** UDP/ICMP interception unchanged (its dispatcher fires on
`InjectInbound`). One item to verify during implementation: on the channel path,
gVisor-generated **outbound** TCP segments leave via `channel.WritePackets` ->
the outbound pump, so `interceptEndpoint.WritePacket` is not the egress path on
darwin (it is on Linux/fdbased). Confirm no outbound logic is lost; if anything
lives in `WritePacket`, move it so both paths share it.

`Stack.New` / `Stack.Run` gain a `startPump(ctx)` call that is a no-op on Linux.

### 3.3 Routes - `internal/route/kernel_darwin.go` + `lookup_darwin.go` (new)

Change `kernel_stub.go` / `lookup_stub.go` build tags to `!linux && !darwin`.
Implement `KernelRoutes` (Add/Remove/List/RouteExists/RefreshLink/LinkExists)
against a `PF_ROUTE` socket:
- **Add/Remove:** write `RTM_ADD` / `RTM_DELETE` `rt_msghdr` messages with
  `RTF_STATIC | RTF_UP` and an interface-scope gateway (`RTA_GATEWAY` =
  `sockaddr_dl` for the utun index) - the kernel equivalent of
  `route add -net CIDR -interface utunN` (proven working via CLI).
- **List / RouteExists:** dump the RIB with `sysctl(NET_RT_DUMP)` and parse with
  `golang.org/x/net/route` (already an indirect-friendly, pure-Go parser).
- `route/manager.go`, `plan.go`, `conflict.go`, `state.go` are
  platform-independent and stay as-is.
- First-cut fallback (acceptable, proven): shell `/sbin/route` for add/delete
  and `route -n get` for lookup. Recommend the socket implementation for parity
  with the netlink-quality Linux path.

### 3.4 DNS host integration - `internal/dns/resolver_darwin.go` (new)

macOS uses split-DNS resolver files instead of resolvectl/dnsmasq. Add an
`Integration` named `"etc-resolver"`:
- `Apply(suffixes)`: for each TLD write `/etc/resolver/<tld>` containing
  `nameserver <listenIP>` and `port 53`; then
  `dscacheutil -flushcache && killall -HUP mDNSResponder`. Atomic temp+rename.
- `Revert()`: remove the files Mutiauk created (track them), flush again.
  Idempotent, for `RevertAll` at startup and `cleanup`.
- `integration_other.go` and `hoststate_other.go` tags -> `!linux && !darwin`.
  Add `dns/hoststate_darwin.go` reading `/etc/resolver/*` for `dns status`.
- `DetectIntegration` (in `dns/integration.go`, the cross-platform file) selects
  this on darwin; `dns_integration: none` still forces noop.

### 3.4.1 Autoroutes - no darwin code required (verified)

Autoroutes is a composition, not a platform layer. `internal/autoroutes/*`
has zero platform-specific imports (no netlink, no `syscall`, no build tags);
its only internal dependency is `internal/route`. The poller/client are pure Go
HTTP. The DNS resolver (`dns/server.go`) binds via `miekg/dns` to a plain
`IP:port` with no `SO_BINDTODEVICE`/socket-control hooks, so it is portable as-is
(the spike proved the utun IP is reachable from the host).

Therefore autoroutes works on darwin once 3.3 (routes) and 3.4 (DNS integration)
land - no `autoroutes_darwin.go`.

One requirement to honor: the poller updates the **active TLD suffix set every
tick**, so the darwin `/etc/resolver` integration's `Apply(suffixes)` must
**reconcile** the resolver-file set to the requested suffixes (create new TLDs,
remove stale ones), not write once. This already fits the daemon's
`applyDNSIntegration`, which calls `Apply` with the new set and diff-caches on
`(listenIP, suffixes)`.

### 3.5 Service runner - `internal/service/service_darwin.go` (new)

Replace the systemd guards. In `service/service.go` change the four
`runtime.GOOS != "linux"` checks (lines ~50, 76, 89, 115) to
`!= "linux" && != "darwin"`. Change `service_other.go` tag to `!linux && !darwin`.

`service_darwin.go` implements the `*Impl` functions against launchd (all
verified):
- **Install:** write `/Library/LaunchDaemons/<label>.plist` (root:wheel, 0644)
  with `ProgramArguments` = `[binary, --config, <path>, daemon, start]` (or a
  foreground daemon mode), `RunAtLoad=true`, `KeepAlive=true`,
  `StandardOutPath`/`StandardErrorPath`, `ProcessType=Interactive` or
  `Background`. Then `launchctl bootstrap system <plist>` and
  `launchctl enable system/<label>`.
- **Uninstall:** `launchctl bootout system/<label>` then remove the plist.
- **Status / StatusDetailed:** `launchctl print system/<label>`; map
  `state = running` -> `Active`, the `state` token -> `SubState`, presence of
  the plist -> `Installed`, `launchctl print-disabled`/`enable` state ->
  `Enabled`. (`print` returns "Bad request"/nonzero when absent -> Installed=false.)
- Label: use reverse-DNS, e.g. `com.postalsys.mutiauk` (matches the docs
  domain) to avoid colliding with the systemd unit name.

Note: launchd has no `Type=notify`/`sd_notify`. `daemon status` readiness on
darwin should rely on the pid/uptime check that already backs the k8s-readiness
path, not on a notify socket.

### 3.6 Daemon wiring - `internal/daemon/server.go`

- The route manager is built from `cfg.TUN.Name` (line ~216) **before**
  `tun.New` (line ~257). On darwin the real name is only known after open.
  Reorder so the route manager (and DNS bind) use `tunDev.Name()`, or add a
  post-create `routeMgr.SetTUNName(actual)` / `RefreshLink`. Low risk - the
  manager already has `RefreshLink`.
- `startPump(ctx)` (3.2) wired into the stack startup; no-op on Linux.
- SIGHUP reload path is unaffected.

### 3.7 Build, CLI, release

- **Build tags audit:** every current `//go:build !linux` that represents a
  "real" implementation gap becomes `!linux && !darwin`; darwin gets a real
  file. Files to flip: `tun/device_stub.go`, `route/kernel_stub.go`,
  `route/lookup_stub.go`, `dns/integration_other.go`, `dns/hoststate_other.go`,
  `service/service_other.go`.
- **Makefile:** add `build-darwin` (`GOTOOLCHAIN=go1.25.5 GOOS=darwin
  GOARCH=arm64 go build`) and an `amd64` variant.
- **release.sh:** add `darwin/arm64` + `darwin/amd64` targets. Pure-Go +
  `x/sys` syscalls, no cgo -> cross-compiles cleanly from the `golang:1.25`
  Docker image (`CGO_ENABLED=0`). UPX supports Mach-O but is optional; consider
  skipping UPX for darwin (Gatekeeper friction).
- **CLI copy:** `root.go`/help text and the "Linux only" wording in
  `service.go`, `cleanup.go`, status output should say "Linux/macOS".
- **Signing/notarization:** not required for utun-as-root or CLI use. Flag as a
  later concern only if distributing a signed `.pkg`.

## 4. Testing strategy

- The Docker (Kali) harness cannot test darwin. macOS paths must be tested
  natively on a Mac (root required for utun/route/launchd).
- Add `_darwin_test.go` peers for the new files; keep the cross-platform tests
  (`route_test.go`, `nat_test.go`, `socks5_test.go`, `expose/*_test.go`,
  `config_test.go`) running on both via `make test`.
- Reuse the spike's shape as an integration smoke test gated behind a build tag
  + root check: open utun, route a test CIDR, dial through, assert the
  forwarder sees the connection.
- UDP and ICMP: the spike covered TCP. Add a datagram + ping smoke test on
  darwin since those exercise `intercept_endpoint` + `tunDev.Write` AF framing
  on the response path.

## 5. Effort and sequencing

Recommended order (each independently testable on the Mac):

1. **TUN device + link-endpoint pump** (3.1 + 3.2) - the technical core and
   highest risk; already de-risked by the spike. Gate: a real `mutiauk
   daemon` forwards TCP on darwin.
2. **Routes** (3.3) - unblocks `route`/autoroutes on darwin.
3. **launchd** (3.5) - smallest, fully proven; unblocks `service`.
4. **DNS resolver integration** (3.4) - unblocks autoroutes DNS + `dns status`.
5. **Daemon wiring, build/release, docs** (3.6 + 3.7).

No architectural rewrites; every change slots behind an interface that already
exists. The residual unknowns are MTU/fragmentation edge cases and UDP/ICMP
under real traffic, which only a native Mac test pass will surface.
