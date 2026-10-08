**English** · [Русский](README.ru.md)

# sing-box-lx

**A client core based on sing-box for the desktop launcher, LxBox and routers.** Compatibility
with current Xray and AmneziaWG servers, a daemon and observability for the apps.

- **Compatibility with today's servers and networks.** REALITY with the hybrid post-quantum
  ML-KEM key exchange (X25519MLKEM768) that current Xray requires, XHTTP, VLESS `encryption`,
  AmneziaWG 3.x, ClientHello fragmentation, WARP over MASQUE.
- **A core built to be operated** by demanding consumers — the desktop launcher, the LxBox
  Android app, routers: daemon mode, observability over gRPC, energy saving with sleep for idle
  tunnels, router builds.
- **Extra capabilities.** Multi-hop `chain`, load balancing, DNS server groups, protocol sniffers.
- **Bugs closed where they were found — in real use**, each with a test and a condition for
  retiring the patch.

Details of every item — [Features](#features).

## Table of contents

- [Why the fork exists](#why-the-fork-exists)
- [About sing-box](#about-sing-box)
- [Features](#features)
- [Build](#build)
- [Configuration — a quick tour](#configuration--a-quick-tour)
- [The `lxd` daemon](#the-lxd-daemon)
- [How the fork is maintained](#how-the-fork-is-maintained)
- [Repository map](#repository-map)
- [Links](#links)
- [License](#license)

---

## Why the fork exists

The servers people connect to often run Xray and AmneziaWG and move faster than sing-box;
examples of the lag: REALITY, the post-quantum key exchange, XHTTP, VLESS `encryption`,
AmneziaWG 3.x. `sing-box-lx` closes that gap on its own side without forking away: every
`upstream/stable` release is merged within days, our code lives in its own files behind build
tags, and a build without them is upstream byte for byte — see
[How the fork is maintained](#how-the-fork-is-maintained).

The Go module and the binary keep upstream's name; the `-lx` suffix lives in the version string
only. The rules every feature here lives by — [CONSTITUTION](SPECS/CONSTITUTION.md).

## About sing-box

[sing-box](https://github.com/SagerNet/sing-box) by SagerNet is the universal proxy platform this
core is built on: the protocols, the routing engine, the TUN stack and the `libbox` binding for
mobile all come from there. Its documentation — [sing-box.sagernet.org](https://sing-box.sagernet.org/),
its README — [on GitHub](https://github.com/SagerNet/sing-box/blob/main/README.md).

---

## Features

### Protocols and transports

| Feature | Config surface | What you get | Build tag | Status |
|---|---|---|---|---|
| **XHTTP** — [002](SPECS/FEATURES/002-XHTTP/FEATURE.md) | `transport.type: xhttp` | Xray-compatible "splithttp": modes `auto` / `packet-up` / `stream-up` / `stream-one` over TLS, REALITY or h2c; `xmux` connection reuse; obfuscation options | `with_xhttp` | live-validated against Xray servers; `stream-one` (the `auto`+REALITY path) device-verified |
| **AmneziaWG 2.0 / 3.x** — [003](SPECS/FEATURES/003-AWG/FEATURE.md) | `wireguard` endpoint fields `jc/jmin/jmax`, `s1–s4`, `h1–h4`, `i1–i5`, AWG 3.x `header_protection_key`, padding, trailers, ranged timings | The full obfuscation set of amneziawg-go v3.1, plus WireSock-style **masquerade** sugar `id`/`ip`/`ib` that builds the `I1` decoy for you | `with_awg` | verified against live AWG 2.0 and 3.1 servers; `ip=quic` decoy device-proven against an LTE/WARP DPI |
| **MASQUE / Cloudflare WARP** — [009](SPECS/FEATURES/009-MASQUE_WARP/FEATURE.md) | `type: masque` outbound | CONNECT-IP (RFC 9484) over HTTP/3 or HTTP/2 through a userspace network stack; `profile: cloudflare` for WARP; standard `tls` block; idle-suspend and self-healing reconnect | — | device-verified on Wi-Fi and LTE, `h3` and `h2` |
| **REALITY against current Xray** — [017](SPECS/FEATURES/017-REALITY/FEATURE.md) | `tls.reality` + `tls.utls.fingerprint`, `tls.reality.key_share`, `tls.fragment` / `record_fragment` | The hybrid post-quantum key share `X25519MLKEM768` that Xray ≥ v26.9.8 requires, on `chrome`, `firefox`, `safari` (the latter two via the utls fork submodule); per-node `key_share: classical \| hybrid`; ClientHello fragmentation now applies to REALITY | — (inside `with_utls`) | stand-verified against Xray v26.9.9 and older; `firefox`/`safari` field-confirmed; `key_share` and fragmentation await a field run |
| **VLESS `encryption`** — [012](SPECS/FEATURES/012-VLESS_ENCRYPTION/FEATURE.md) | `encryption` field on a `vless` outbound | Post-quantum `mlkem768x25519plus` layer *inside* VLESS, beneath the transport and independent of TLS/REALITY | — | device-verified: previously dead subscription nodes came alive |

### Routing and DNS

| Feature | Config surface | What you get | Build tag | Status |
|---|---|---|---|---|
| **`chain` outbound** — [015](SPECS/FEATURES/015-CHAIN/FEATURE.md) | `type: chain` | A virtual multi-hop path assembled at runtime from groups and nodes; groups are never copied, hops are runtime links; transparent `direct`, automatic MTU for tunnel links, `strip` / `rewrite` | `with_lx_chain` | live stand on real hops; WireGuard links on device pending |
| **DNS server group** — [013](SPECS/FEATURES/013-DNS_GROUP/FEATURE.md) | `dns.servers[].type: group` | One DNS server over several: `stable` / `fastest` / `parallel` on a TTL model, fan-out with a budget, `survival` visibility | — | shipped; field run pending |
| **Load balancing and failover** — [007](SPECS/FEATURES/007-URLTEST_BALANCE/FEATURE.md) | `urltest` with `mode: round_robin`, `balancer{…}`, `mode: failover`; `least_test` reacts to live dial errors | Round-robin pool with lazy health checks and sticky slots; failover holds the working node until it fails; dead-path errors penalise a node and retry through the best candidate | `with_lx_command` (only `GetPool`) | device-verified on a real multi-node pool |
| **Protocol sniffers** — [016](SPECS/FEATURES/016-SNIFF/FEATURE.md) | `sniff` action names `wireguard`, `openvpn`, `ike`, `tailscale`, `sip` | Recognise other devices' VPN tunnels and calls behind a router by the shape of the first packet; sits ahead of upstream's uTP sniffer that mislabelled WireGuard as bittorrent | — | shipped; router run pending |

### Platform and operations

| Feature | Config surface | What you get | Build tag | Status |
|---|---|---|---|---|
| **Observability** — [006](SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md) | libbox `CommandClient` extensions | `URLTestOutbound`, `GetRules`, `GetGroups`, `GetOutbounds`, `GetPool`, `GetDNSGroups`, `GetRunningConfig`, `GetChains`, `SubscribeDNSQueries`, `Connection.detourList` — what the Android client lives on | `with_lx_command` | shipped, consumed by LxBox |
| **Idle-suspend (energy)** — [008](SPECS/FEATURES/008-ENERGY/FEATURE.md) | `route.lx_idle_suspend` / `lx_idle_suspend_reachable` / `lx_idle_teardown`, `urltest` `mode: failover` | Three sleep levels for idle WireGuard/AWG endpoints: battery, heat and RAM on multi-node mobile profiles | `with_lx_idle_suspend` (baked into the AAR) | device-verified: RSS −31 % |
| **`lxd` daemon** — [014](SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md) | `sing-box lxd` subcommand | The core in-process behind a management channel that outlives every config change: gRPC + admin-REST on one port, `apply` with automatic rollback, mTLS with enrolment, service install, host telemetry | `with_lxd` | device-verified on macOS; OpenWrt installer scripts field-tested |

> **Not supported, by design:** server halves of the above; Xray's post-quantum REALITY **signatures** (`pqv` / ML-DSA-65) and `spiderX` — a different mechanism from the key exchange, absent from sing-box; the `edge`, `ios`, `android`, `360`, `qq` fingerprints against Xray ≥ v26.9.8 (no upstream preset carries the hybrid share, Xray has the same boundary; substituting the fingerprint is the applications' job).

---

## Build

Builds go through **`Makefile.lx`**; the upstream `Makefile` is untouched.

```bash
git clone --recurse-submodules https://github.com/Leadaxe/sing-box-lx
make -f Makefile.lx lx-build        # → ./sing-box, version like vX.Y.Z-lx.N
make -f Makefile.lx lx-check        # validate the sample configs in lx-test/config/
```

- **`--recurse-submodules` is required for every build.** Four dependencies are swapped for fork submodules through `replace` in `go.mod`: `sing-tun`, `gvisor` and `utls` unconditionally, `wireguard-go` (the AmneziaWG runtime) behind `with_awg`. A clone without them does not compile.
- **The tag set** (`make -f Makefile.lx lx-print-tags` is the single source of truth):

  ```
  with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,with_naive_outbound,with_purego,badlinkname,tfogo_checklinkname0,with_xhttp,with_awg,with_lx_command,with_lxd,with_openvpn,with_openconnect,with_lx_chain,with_tailscale
  ```

  That is upstream's client feature set minus the server-only tags (`with_acme`, `with_ccm`/`with_ocm`), plus `with_purego` (CGO-free cross-compile, so `with_naive_outbound` builds at `CGO=0`) and our own tags. `with_lx_command` (libbox command-protocol extensions) and `with_lxd` (the daemon) are independent by design.
- **Toolchain.** The Go version is pinned in **`go.version`** and read by every CI `setup-go` step; the upstream version the fork is based on lives in **`upstream.version`**. Not `go-version-file: go.mod` — that would resolve to a language floor, not a toolchain, and a Go 1.24 AAR kills every quic-go outbound on Android.
- **Android (`libbox.aar`).** `make lib_install && make lib_android` builds `libbox.aar` (SDK 23) and `libbox-legacy.aar` (SDK 21) with `with_xhttp` / `with_awg` / `with_lx_command` / `with_lx_idle_suspend` / `with_lx_chain` / `with_tailscale` baked in. `clash_api` is dropped from the AAR only — the Android client manages the core over the native `CommandClient`.
- **Release matrix** (`lx-release.yml` on a `v*-lx.*` tag): desktop binaries for linux / darwin / windows incl. a **Windows 7 (32-bit)** legacy build (without naive and `lxd`), linux-musl and mips/mipsle softfloat for routers, both AARs, `SHA256SUMS`. Release notes come from `docs-lx/releases/`.

---

## Configuration — a quick tour

One snippet per feature. Field tables, defaults and every option — **[docs-lx/lx-config.md](docs-lx/lx-config.md)** ([RU](docs-lx/lx-config.ru.md)); wire-level detail for XHTTP, AmneziaWG and MASQUE — **[docs-lx/protocols-transports.md](docs-lx/protocols-transports.md)** ([RU](docs-lx/protocols-transports.ru.md)).

### XHTTP transport

```jsonc
"transport": { "type": "xhttp", "host": "example.com", "path": "/xhttp", "mode": "auto" }   // auto | packet-up | stream-up | stream-one
```

### AmneziaWG endpoint

```jsonc
{
  "type": "wireguard",                       // … standard wireguard fields …
  "jc": 10, "jmin": 50, "jmax": 100,
  "s1": 20, "s2": 20, "s3": 60, "s4": 60,
  "h1": 1, "h2": 2, "h3": "1000-2000", "h4": 4,   // single value or "N-M" range
  "i1": "<b 0x...><r 12>",                          // I1–I5: must match the server, case-sensitive
  "id": "www.google.com", "ip": "quic", "ib": "chrome"   // or: masquerade sugar instead of a hand-written i1
}
```

`id`/`ip`/`ib` and an explicit `i1` are mutually exclusive. `ip=quic` sends two out-of-order fragmented QUIC Initials and is the profile proven against a live DPI; `dns`/`stun`/`sip` are correct requests kept for providers whose DPI only checks well-formedness. Reference — [protocols-transports.md §2](docs-lx/protocols-transports.md#2-amneziawg-203x-awg2-awg3) ([RU](docs-lx/protocols-transports.ru.md#2-amneziawg-203x-awg2-awg3)) · [masquerade examples](SPECS/TASKS/009-WIRESOCK_MASQUERADE_PROFILES/EXAMPLES.md).

### MASQUE outbound (Cloudflare WARP)

```jsonc
{
  "type": "masque", "tag": "warp",
  "server": "162.159.198.2", "server_port": 443,
  "profile": "cloudflare",                        // cloudflare (WARP) | standard (RFC 9484)
  "vhttp": "h3",                                  // h3 (QUIC) | h2 (HTTP/2, for networks that filter UDP:443)
  "tls": { "server_name": "www.microsoft.com" },  // fronting SNI; auth is public-key pinning
  "private_key": "<base64 DER EC>", "public_key": "<base64 DER PKIX>",
  "ip": "172.16.0.2/32", "ipv6": "2606:4700:110:...::/128"
}
```

Key material comes from the WARP device registration done by the client. Not to be confused with the AWG *masquerade* sugar above — same word, different feature. Reference — [protocols-transports.md §3](docs-lx/protocols-transports.md#3-masque-outbound-connect-ip--warp) ([RU](docs-lx/protocols-transports.ru.md#3-masque-outbound-connect-ip--warp)).

### REALITY: fingerprint and `key_share`

```jsonc
"tls": {
  "enabled": true, "server_name": "www.apple.com",
  "utls": { "enabled": true, "fingerprint": "chrome" },      // chrome | firefox | safari carry the hybrid share
  "reality": {
    "enabled": true, "public_key": "<base64url>", "short_id": "0123abcd",
    "key_share": ""    // "" = as the fingerprint carries it · "classical" = strip X25519MLKEM768 (Xray < v26.9.8 only, one TCP segment) · "hybrid" = require it
  }
}
```

`classical` exists for networks that drop the two-segment hybrid ClientHello; on a newer server the remaining levers are `record_fragment` (now effective on REALITY) and a `detour`. Reference — [lx-config.md §7](docs-lx/lx-config.md#7-reality-key_share--hybrid-or-classical-clienthello-spec-089) ([RU](docs-lx/lx-config.ru.md#7-reality-key_share--гибридный-или-классический-clienthello-spec-089)).

### VLESS `encryption`

```jsonc
{ "type": "vless", "uuid": "…", "encryption": "mlkem768x25519plus.native.0rtt.<ML-KEM-768 key>" }   // absent or "none" = off
```

Client half only; `decryption` is server-side and deliberately not ported. Reference — [lx-config.md §6](docs-lx/lx-config.md#6-vless-encryption--post-quantum-layer-spec-032) ([RU](docs-lx/lx-config.ru.md#6-vless-encryption--пост-квантовый-слой-spec-032)).

### DNS server group

```jsonc
{ "type": "group", "tag": "dns-public", "mode": "stable", "servers": ["dns-cf", "dns-google", "dns-quad9"] }   // stable | fastest | parallel
```

Reference — [lx-config.md §5](docs-lx/lx-config.md#5-dns-server-group-spec-033035) ([RU](docs-lx/lx-config.ru.md#5-группа-dns-серверов-spec-033035)).

### `chain` outbound

```jsonc
{
  "type": "chain", "tag": "virtualisation",
  "outbounds": ["selector-in", "selector-mid", "selector-exit"],   // entry → exit, in packet order
  "idle_timeout": "5m",
  "strip": { "multiplex.padding": false },                          // one-sided DPI tricks are stripped from links by default
  "rewrite": { "wireguard": { "mtu": 1200 } }                        // merge-patch per node type, links only
}
```

Tunnel links get their MTU lowered automatically; the path shows in `detourList` and `GetChains`, per-layer latency via URLTest on the hop tags `<tag>#0`, `<tag>#1`, …. Reference — [lx-config.md §10](docs-lx/lx-config.md#10-chain-outbound--a-virtual-multi-hop-path-of-groups-and-nodes-spec-073) ([RU](docs-lx/lx-config.ru.md#10-outbound-chain--виртуальная-цепочка-хопов-из-групп-и-узлов-spec-073)).

### Balancing, energy, sniffers

No new types — a few fields on existing ones: `urltest` `mode: round_robin` + `balancer{…}` and `mode: failover` ([lx-config.md §3](docs-lx/lx-config.md#3-urltest-node-selection-modes-spec-019--116), [RU](docs-lx/lx-config.ru.md#3-режимы-выбора-узла-в-urltest-spec-019--116)); `route.lx_idle_*` sleep levels ([lx-energy.md](docs-lx/lx-energy.md), [RU](docs-lx/lx-energy.ru.md)); protocol names in the `sniff` action and `protocol` rules ([lx-sniff.md](docs-lx/lx-sniff.md), [RU](docs-lx/lx-sniff.ru.md)).

---

## The `lxd` daemon

`sing-box lxd` (build tag `with_lxd`) hosts the core **in-process** behind a control channel that belongs to the daemon rather than to the box instance, so it survives every config change and is reachable exactly when the data plane is down.

```bash
sing-box lxd --state-dir ./lxd-state -c config.json
```

- **Reload without losing the channel** — `POST /admin/apply` validates the candidate in a subprocess, swaps the instance and promotes it to *last-good* only after a successful start; a failed start rolls back automatically.
- **One port, two planes** — gRPC (the same `CommandClient` contract the Android client speaks) and admin-REST (plain stdlib client, Windows 7 friendly).
- **mTLS with enrolment** — the daemon is its own CA, prints an `address#fingerprint#code` invite, and knows clients by certificate afterwards.
- **Observability without a second port** — memory, stats, logs, pprof, host telemetry (CPU per core, memory, thermal, disks, interfaces) and an IP → device directory.
- **Service install** on macOS; on Linux (systemd, OpenWrt/procd) the daemon prints the recipe instead of touching the disk.

📖 Operator's guide — **[docs-lx/lxd-daemon.md](docs-lx/lxd-daemon.md)** ([RU](docs-lx/lxd-daemon.ru.md)); the client-facing observability contract — [docs-lx/lxd-grpc-api.md](docs-lx/lxd-grpc-api.md) ([RU](docs-lx/lxd-grpc-api.ru.md)); OpenWrt walkthrough (VPN on a dedicated SSID) — [docs-lx/openwrt-vpn-ssid.md](docs-lx/openwrt-vpn-ssid.md) ([RU](docs-lx/openwrt-vpn-ssid.ru.md)) with installer scripts in [`scripts-lx/openwrt/`](scripts-lx/openwrt/README.md).

---

## How the fork is maintained

```
upstream/stable  ──merge──►  lx  =  upstream  +  // lx seams  +  lx-owned files  +  4 fork submodules
                                     │
                                     └─►  tag vX.Y.Z-lx.N  ──►  lx-release.yml  ──►  GitHub Release
```

- **Manual merge of `upstream/stable`, never a rebase.** `lx` is both the working and the release branch and is never force-pushed. Drift is measured only by merge-base against `upstream/stable`; the GitHub "N commits behind testing" banner is not drift.
- **Fork submodules are part of the delta**: [wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx) (AmneziaWG runtime), [sing-tun-lx](https://github.com/Leadaxe/sing-tun-lx) (accept-loop self-heal), [gvisor-lx](https://github.com/Leadaxe/gvisor-lx) (handshake nil-guard), [utls-lx](https://github.com/Leadaxe/utls-lx) (Firefox 148 and Safari 26.3 presets). Each is its upstream plus a few commits; submodule drift is closed **before** the core merge.
- **Hotfixes for upstream bugs carry an expiry**: every patch in the [HOTFIXES registry](SPECS/FEATURES/004-HOTFIXES/FEATURE.md) names the condition under which it is removed.
- **Releases**: tags `vX.Y.Z-lx.N` are stable, `-rc.N` / `-alpha.N` / `-beta.N` are pre-releases; the procedure is the [release runbook](docs-lx/lx-release-runbook.md) ([RU](docs-lx/lx-release-runbook.ru.md)); the engineering log is [lx-changelog.md](docs-lx/lx-changelog.md), user-facing notes are in [`docs-lx/releases/`](docs-lx/releases/).
- **Spec Kit**: [`SPECS/FEATURES`](SPECS/FEATURES/README.md) describes each feature's current state as a black box; [`SPECS/TASKS`](SPECS/README.md) holds one folder per unit of work (`SPEC → PLAN → TASKS → report`), with a roadmap and status codes; [CONSTITUTION](SPECS/CONSTITUTION.md) holds the rules.
- **Remotes**: `origin` = `Leadaxe/sing-box-lx` (default branch `lx`), `upstream` = `SagerNet/sing-box`.

### Consumers

| Consumer | Platform | What it takes from here |
|---|---|---|
| [singbox-launcher](https://github.com/Leadaxe/singbox-launcher) | desktop | the `sing-box` binary (bundled as `bin/sing-box`), optionally the `lxd` daemon |
| [LxBox](https://github.com/Leadaxe/LxBox) | Android | `libbox.aar` and the `CommandClient` extensions |
| OpenWrt routers | mips / musl builds | the binary as `lxd` with the installer scripts |

Mapping subscription links to config fields is a consumer-side job; the same config JSON applies everywhere.

---

## Repository map

Everything downstream is either a new file or a seam marked `// lx`; `grep -rn "lx:begin"` finds every seam in an upstream file.

| Path | Purpose |
|------|---------|
| `Makefile.lx` | build with the lx tag set and the `-lx` version; `lx-build`, `lx-check`, `lx-print-tags`, `lx-proto` |
| `go.version` / `upstream.version` | pinned Go toolchain / the upstream version the fork is based on |
| `.github/workflows/lx-ci.yml`, `lx-release.yml`, `lx-build.yml` | CI matrix, release on `v*-lx.*` tags, on-demand builder for any branch |
| `SPECS/` | Spec Kit: `FEATURES/` (state), `TASKS/` (work), `CONSTITUTION.md` |
| `docs-lx/` | fork documentation (EN + RU), changelog, release notes |
| `lx-test/` | sample configs for `sing-box check` and live stands (`zombie`, `chain`, …) |
| `scripts-lx/openwrt/` | router installer for `lxd` |
| `transport/v2rayxhttp/` | XHTTP client transport |
| `transport/wireguard/device_awg.go`, `submodules/wireguard-go` | AmneziaWG parameters and runtime |
| `protocol/masque/` | MASQUE / CONNECT-IP outbound |
| `protocol/chain/` | `chain` outbound |
| `common/tls/` (`*_lx*`), `submodules/utls` | REALITY key share, fragmentation, fingerprint presets |
| `common/sniff/*_lx.go` | protocol sniffers |
| `dns/transport/group/`, `common/dnstrack/` | DNS server group and the DNS query trace behind `SubscribeDNSQueries` |
| `experimental/libbox/`, `daemon/` (`lx:` seams) | `CommandClient` extensions |
| `lxd/` | the `lxd` daemon |
| `option/v2ray_xhttp.go`, `option/wireguard_awg.go`, `option/masque_lx.go`, `option/chain_lx.go` | feature options |
| `submodules/sing-tun`, `submodules/gvisor` | fork submodules for the TUN stack |

---

## Links

| | |
|---|---|
| Upstream | [SagerNet/sing-box](https://github.com/SagerNet/sing-box) · [docs](https://sing-box.sagernet.org/) |
| Config overview | [docs-lx/lx-config.md](docs-lx/lx-config.md) ([RU](docs-lx/lx-config.ru.md)) — every field of every feature, with examples |
| Protocols & transports | [docs-lx/protocols-transports.md](docs-lx/protocols-transports.md) ([RU](docs-lx/protocols-transports.ru.md)) — XHTTP, AmneziaWG, MASQUE, REALITY, VLESS `encryption` in depth |
| How the Xray protocols work | [docs-lx/xray-protocols-explained.md](docs-lx/xray-protocols-explained.md) ([RU](docs-lx/xray-protocols-explained.ru.md)) — REALITY, Vision, XHTTP, VLESS `encryption`: mechanics, the fork's implementation, differences from vanilla, Xray ↔ sing-box-lx examples |
| How AmneziaWG works | [docs-lx/amneziawg-explained.md](docs-lx/amneziawg-explained.md) ([RU](docs-lx/amneziawg-explained.ru.md)) — three obfuscation layers, the MTU budget, the graft in the wireguard-go fork, `awg.conf` ↔ endpoint example |
| Energy guide | [docs-lx/lx-energy.md](docs-lx/lx-energy.md) ([RU](docs-lx/lx-energy.ru.md)) — idle-suspend levels, `mode: failover`, tuning |
| Sniffers | [docs-lx/lx-sniff.md](docs-lx/lx-sniff.md) ([RU](docs-lx/lx-sniff.ru.md)) |
| `lxd` operator's guide | [docs-lx/lxd-daemon.md](docs-lx/lxd-daemon.md) ([RU](docs-lx/lxd-daemon.ru.md)) |
| Observability API | [docs-lx/lxd-grpc-api.md](docs-lx/lxd-grpc-api.md) ([RU](docs-lx/lxd-grpc-api.ru.md)) — the contract clients speak, gRPC daemon and Android AAR alike |
| OpenWrt walkthrough | [docs-lx/openwrt-vpn-ssid.md](docs-lx/openwrt-vpn-ssid.md) ([RU](docs-lx/openwrt-vpn-ssid.ru.md)) |
| Release runbook | [docs-lx/lx-release-runbook.md](docs-lx/lx-release-runbook.md) ([RU](docs-lx/lx-release-runbook.ru.md)) |
| Changelog & release notes | [docs-lx/lx-changelog.md](docs-lx/lx-changelog.md) · [docs-lx/releases/](docs-lx/releases/) |
| Reference cores | [docs-lx/lx-reference-cores.md](docs-lx/lx-reference-cores.md) ([RU](docs-lx/lx-reference-cores.ru.md)) — where to look for wire-protocol answers |
| Spec Kit | [SPECS/FEATURES](SPECS/FEATURES/README.md) · [SPECS/TASKS](SPECS/README.md) · [CONSTITUTION](SPECS/CONSTITUTION.md) |
| Protocol origins | [XTLS/Xray-core](https://github.com/XTLS/Xray-core) (XHTTP, REALITY, VLESS encryption) · [amnezia-vpn/amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) · [Cloudflare WARP / MASQUE](https://developers.cloudflare.com/warp-client/) |

---

## License

Inherits the upstream sing-box license (**GPL-3.0**). All edits are marked `// lx` and distributed under the same license. This is an unofficial fork, not affiliated with SagerNet.
