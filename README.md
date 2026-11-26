**English** · [Русский](README.ru.md)

# sing-box-lx — a sing-box fork for current Xray, AmneziaWG and Cloudflare WARP servers

[![Latest release](https://img.shields.io/github/v/release/Leadaxe/sing-box-lx?label=release)](https://github.com/Leadaxe/sing-box-lx/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/Leadaxe/sing-box-lx/total)](https://github.com/Leadaxe/sing-box-lx/releases)
[![Go toolchain](https://img.shields.io/badge/Go-1.26.10-00ADD8?logo=go&logoColor=white)](go.version)
[![License: GPL-3.0](https://img.shields.io/github/license/Leadaxe/sing-box-lx)](LICENSE)

**sing-box-lx is an extended client proxy and VPN core based on [sing-box](https://github.com/SagerNet/sing-box) for desktop, Android and routers, compatible with current Xray and AmneziaWG servers.** The fork adds what those servers ask of a client: REALITY with the post-quantum key share, the XHTTP transport and VLESS `encryption` from Xray, AmneziaWG 2.0 / 3.x on the WireGuard endpoint, Cloudflare WARP over MASQUE, and TrustTunnel. Around the protocols it adds what apps and OpenWrt routers need to run a core: the `lxd` daemon, a gRPC state API, sleep for idle tunnels.

It is a drop-in: the binary is still `sing-box`, the config is still sing-box JSON, and `-lx` appears only in the version string. Every `upstream/stable` release is merged; a build without the fork's tags and a config without its fields behave as upstream, byte for byte.

The project is free and developed in spare time — **[support it](docs-lx/donate.md)** if it is useful to you.

## What you get

- **Nodes that died after the server updated Xray connect again.** Xray ≥ v26.9.8 rejects a REALITY ClientHello without the hybrid key share `X25519MLKEM768`; the core sends it on the `chrome`, `firefox` and `safari` fingerprints. → [REALITY](#reality-works-with-current-xray-servers-including-post-quantum-x25519mlkem768)
- **WireGuard gets through a network that drops it by signature.** AmneziaWG changes the shape of the packets, and the masquerade keys put a browser's QUIC Initial ahead of the handshake. → [AmneziaWG](#amneziawg-20-and-3x-run-on-the-standard-wireguard-endpoint)
- **Cloudflare WARP connects where UDP 443 is filtered.** The `masque` outbound carries the tunnel over HTTP/3 or over HTTP/2 on TCP 443. → [MASQUE](#cloudflare-warp-connects-over-masque-on-http3-or-http2)
- **A phone with ten WireGuard nodes stops paying for nine idle ones.** Idle endpoints go to sleep: on a device, eight sleeping endpoints freed 134 MB. → [Idle-suspend](#idle-wireguard-tunnels-sleep-on-android)
- **A router serves VPN on a separate SSID and rolls back if a config breaks it.** The `lxd` daemon checks a config before the swap and returns to the last one that worked if the core does not start; the control channel stays up all the while. → [`lxd`](#the-lxd-daemon-keeps-its-control-channel-through-every-config-change)
- **An app shows what the core is doing without parsing logs.** Groups, pools, chains, rules, DNS queries and per-connection paths come over gRPC. → [Observability](#observability-over-grpc-the-app-sees-what-the-core-is-doing)
- **A pool server that goes down drops no requests.** The `round_robin` load balancer retries the connection through a neighbouring node and replaces the dead node with a live one from the reserve without waiting for a check. → [`urltest`](#urltest-balances-across-a-pool-or-holds-one-node-until-it-fails)
- **Balancing, failover and transports are tested on a bad network without root.** The `impair` outbound degrades the link inside the core under control: rate, loss, scheduled outages, the same run again by `seed`. → [Network impairment](#network-impairment-without-root-a-controlled-bad-link-inside-the-core)

## Download and install

Binaries are on **[GitHub Releases](https://github.com/Leadaxe/sing-box-lx/releases/latest)**; every release carries `SHA256SUMS`. Archives are named `sing-box-<version>-<target>`.

| Runs on | Target | Notes |
|---|---|---|
| Windows x64 / ARM64 | `windows-amd64.zip`, `windows-arm64.zip` | keep `libcronet.dll` from the archive next to `sing-box.exe` — the `naive` outbound loads it |
| Windows 7, 32-bit | `windows-386-legacy-windows-7.zip` | without `naive` and `lxd` |
| macOS, Intel / Apple silicon | `darwin-amd64.tar.gz`, `darwin-arm64.tar.gz` | |
| Linux and routers: OpenWrt, Keenetic, AsusWRT Merlin | `linux-amd64`, `linux-arm64`, `linux-armv7`, `linux-mipsle-softfloat`, `linux-386`, `linux-riscv64` | static musl builds, no glibc needed |
| Big-endian MIPS routers (OpenWrt `mips_24kc`) | `linux-mips-softfloat` | without `naive` |
| Routers with a small flash overlay | `linux-arm64.upx`, `linux-mipsle-softfloat.upx` | 17 MB on flash instead of 75–90 MB; the binary unpacks into RAM at every start |
| Android apps | `libbox-<version>.aar` (SDK 23), `libbox-legacy-<version>.aar` (SDK 21) | gomobile library, not an app |

Looking for an app, not a core: [singbox-launcher](https://github.com/Leadaxe/singbox-launcher) for desktop and [LxBox](https://github.com/Leadaxe/LxBox) for Android ship this core. On OpenWrt one [installer script](scripts-lx/openwrt/README.md) downloads the binary with a checksum check and sets up the daemon.

### Build from source

```bash
git clone --recurse-submodules https://github.com/Leadaxe/sing-box-lx
make -f Makefile.lx lx-build        # → ./sing-box, version like vX.Y.Z-lx.N
make -f Makefile.lx lx-check        # validate the sample configs in lx-test/config/
```

- **`--recurse-submodules` is required.** Four dependencies are replaced by fork submodules through `replace` in `go.mod`: `sing-tun`, `gvisor` and `utls` always, `wireguard-go` (the AmneziaWG runtime) behind `with_awg`. A clone without them does not compile.
- **Tags.** `make -f Makefile.lx lx-print-tags` prints the set: upstream's client tags without the server-only ones (`with_acme`, `with_ccm` / `with_ocm`), plus `with_purego` and the fork's `with_xhttp`, `with_awg`, `with_lx_command`, `with_lxd`, `with_lx_chain`, `with_trusttunnel`, `with_lx_impair`. The upstream `Makefile` is untouched.
- **Toolchain.** The Go version is pinned in [`go.version`](go.version) and read by every CI job; the upstream version the fork is based on is in [`upstream.version`](upstream.version).
- **Android.** `make lib_install && make lib_android` builds both AARs. The AAR has no `clash_api` and no `lxd`: an Android app talks to the core over the native `CommandClient`.

## Features

One module per feature: what it gives, how, an example, the limits. Every field and default — [docs-lx/lx-config.md](docs-lx/lx-config.md) ([RU](docs-lx/lx-config.ru.md)); wire-level detail — [docs-lx/protocols-transports.md](docs-lx/protocols-transports.md) ([RU](docs-lx/protocols-transports.ru.md)); the index of features with their specs — [SPECS/FEATURES](SPECS/FEATURES/README.md).

### REALITY works with current Xray servers, including post-quantum X25519MLKEM768

A node on a server updated to Xray ≥ v26.9.8 connects instead of failing with `reality verification failed`. Such a server accepts only a ClientHello that carries the hybrid key share `X25519MLKEM768` ahead of `X25519`. The core sends it on `chrome`, on `firefox` and `safari` (Firefox 148 and Safari 26.3 presets from the [utls-lx](https://github.com/Leadaxe/utls-lx) fork) and on the opt-in `chrome_155`.

```jsonc
"tls": {
  "enabled": true, "server_name": "www.apple.com",
  "utls": { "enabled": true, "fingerprint": "chrome" },
  "reality": { "enabled": true, "public_key": "<base64url>", "short_id": "0123abcd",
               "key_share": "" }   // "" = as the fingerprint carries it · "classical" · "hybrid"
}
```

Limits: the hybrid ClientHello is 1.7 KB and leaves as two TCP segments; on a network that loses the second one, use `record_fragment` (it applies to REALITY here) or a `detour`, and `key_share: classical` for a server older than v26.9.8. The `edge`, `ios`, `android`, `360`, `qq` fingerprints have no preset with the hybrid share and are rejected by a current Xray — pick one of the three above. Xray's post-quantum REALITY signatures (`pqv` / ML-DSA-65) and `spiderX` are not supported.

No build tag (inside `with_utls`) · verified on a stand against Xray v26.9.9 and older; `firefox` / `safari` confirmed in the field · [feature](SPECS/FEATURES/017-REALITY/FEATURE.md) · [config](docs-lx/lx-config.md#7-reality-key_share--hybrid-or-classical-clienthello) ([RU](docs-lx/lx-config.ru.md#7-reality-key_share--гибридный-или-классический-clienthello))

### XHTTP connects to Xray servers over TLS, REALITY or h2c

A VLESS node with Xray's XHTTP ("splithttp") transport works from the same config as any other sing-box node. The transport sends uplink and downlink as ordinary HTTP requests in the modes `auto`, `packet-up`, `stream-up` and `stream-one`, and reuses connections through `xmux` the way a browser does.

```jsonc
"transport": { "type": "xhttp", "host": "example.com", "path": "/xhttp", "mode": "auto" }
```

Limits: client side only. A mismatch with the server shows up as silence, not as an error — the checklist is in [protocols-transports.md §1.11](docs-lx/protocols-transports.md#111-troubleshooting) ([RU](docs-lx/protocols-transports.ru.md#111-диагностика)).

`with_xhttp` · verified against live Xray servers; `stream-one` under REALITY on a device · [feature](SPECS/FEATURES/002-XHTTP/FEATURE.md)

### VLESS `encryption` adds a post-quantum layer inside VLESS

Nodes whose server sets Xray's `decryption` connect; without the field they do not answer. The `mlkem768x25519plus` layer sits between VLESS and the transport and does not depend on TLS or REALITY.

```jsonc
{ "type": "vless", "uuid": "…", "encryption": "mlkem768x25519plus.native.0rtt.<ML-KEM-768 key>" }   // absent or "none" = off
```

Limits: the client half only; the server-side `decryption` is not implemented — run Xray on the server.

No build tag · verified on a device: subscription nodes that did not answer came alive · [feature](SPECS/FEATURES/012-VLESS_ENCRYPTION/FEATURE.md) · [config](docs-lx/lx-config.md#6-vless-encryption--post-quantum-layer) ([RU](docs-lx/lx-config.ru.md#6-vless-encryption--пост-квантовый-слой))

### AmneziaWG 2.0 and 3.x run on the standard WireGuard endpoint

A tunnel works on a network that recognises WireGuard by packet sizes and headers. The `wireguard` endpoint takes the full parameter set of amneziawg-go v3.1 — junk packets, padded and re-typed headers, decoy packets `i1`–`i5`, AWG 3.x header protection — so an `awg.conf` maps to it field by field. The masquerade keys `id` / `ip` / `ib` build the decoy for you: `ip: quic` sends a QUIC Initial with a whole browser ClientHello before every handshake.

```jsonc
{
  "type": "wireguard",                              // … standard wireguard fields …
  "jc": 10, "jmin": 50, "jmax": 100,
  "s1": 20, "s2": 20, "s3": 60, "s4": 60,
  "h1": 1, "h2": 2, "h3": "1000-2000", "h4": 4,     // a value or an "N-M" range
  "id": "www.google.com", "ip": "quic", "ib": "chrome"   // or a hand-written "i1"; the two are mutually exclusive
}
```

Limits: `s1`–`s4`, `h1`–`h4` and, on AWG 3.x, `header_protection_key` must match the server exactly, or the handshake stays silent; junk and decoys are set on the sending side only. A config without AWG fields is a plain WireGuard endpoint.

`with_awg` · verified against live AWG 2.0 and 3.1 servers; the `ip: quic` decoy brought up Cloudflare WARP on an LTE network where plain WireGuard did not connect · [feature](SPECS/FEATURES/003-AWG/FEATURE.md) · [reference](docs-lx/protocols-transports.md#2-amneziawg-203x-awg2-awg3) ([RU](docs-lx/protocols-transports.ru.md#2-amneziawg-203x-awg2-awg3)) · [masquerade examples](SPECS/TASKS/009-WIRESOCK_MASQUERADE_PROFILES/EXAMPLES.md) · [how AmneziaWG works](docs-lx/amneziawg-explained.md) ([RU](docs-lx/amneziawg-explained.ru.md))

### Cloudflare WARP connects over MASQUE on HTTP/3 or HTTP/2

WARP works where its WireGuard endpoint does not answer, and with `vhttp: h2` also where UDP 443 is filtered. The `masque` outbound carries whole IP packets with CONNECT-IP ([RFC 9484](https://www.rfc-editor.org/rfc/rfc9484)) through a userspace network stack, sleeps while idle and reconnects by itself.

```jsonc
{
  "type": "masque", "tag": "warp",
  "server": "162.159.198.2", "server_port": 443,
  "profile": "cloudflare",                        // cloudflare (WARP) | standard (RFC 9484)
  "vhttp": "h3",                                  // auto (default) | h3 (QUIC) | h2 (TCP 443)
  "tls": { "server_name": "www.microsoft.com" },
  "private_key": "<base64 DER EC>", "public_key": "<base64 DER PKIX>",
  "ip": "172.16.0.2/32", "ipv6": "2606:4700:110:...::/128"
}
```

Limits: the core does not register a WARP device — the keys come from the client app or a converter. An HTTP proxy cannot be the `detour` for `h3`, which is UDP; use `h2` there. MASQUE is not the AmneziaWG *masquerade* above: same word, different feature.

No build tag (needs `with_quic`, `with_gvisor`) · verified on a device on Wi-Fi and LTE, `h3` and `h2` · [feature](SPECS/FEATURES/009-MASQUE_WARP/FEATURE.md) · [config](docs-lx/lx-config.md#4-masque-outbound--cloudflare-warp) ([RU](docs-lx/lx-config.ru.md#4-masque-outbound--cloudflare-warp)) · [reference](docs-lx/protocols-transports.md#3-masque-outbound-connect-ip--warp) ([RU](docs-lx/protocols-transports.ru.md#3-masque-outbound-connect-ip--warp))

### TrustTunnel works as a client and as a server

One core connects to an AdGuard TrustTunnel endpoint and can be one. `type: trusttunnel` is a proxy `CONNECT` with Basic auth over HTTP/2 (TLS, uTLS) or HTTP/3 (QUIC): TCP per stream, UDP in `_udp2` frames, one session per node that is silent while idle. The inbound keeps the limits of the official endpoint and answers probes with 404.

Limits: interoperability with the official endpoint 1.1.0 and client 1.1.11 is verified on a stand in both directions; there has been no run on a live VPS yet. It is the only inbound the fork adds — for the server side of the other protocols run Xray or AmneziaWG.

`with_trusttunnel` · [feature](SPECS/FEATURES/018-TRUSTTUNNEL/FEATURE.md) · [reference](docs-lx/protocols-transports.md#6-trusttunnel-outbound-and-inbound) ([RU](docs-lx/protocols-transports.ru.md#6-trusttunnel-outbound-и-inbound)) · [how TrustTunnel works](docs-lx/trusttunnel-protocols-explained.md) ([RU](docs-lx/trusttunnel-protocols-explained.ru.md))

### `sniff_guard` keeps nodes with a disguise SNI working behind relays that reroute by SNI

A REALITY or WARP node behind a `detour` relay stops looking dead. A relay or a home router with an Xray inbound and `sniffing.destOverride` reads the name from the first handshake packet and sends the connection there; with a disguise SNI that is someone else's host. `lx.sniff_guard` makes the first packet of a TLS or QUIC handshake unreadable to the sniffer — a split ClientHello, a delay, a decoy byte ahead of the QUIC Initial — without changing what the server receives, and learns from connection outcomes which paths need it.

Limits: off until the config has the block; the defaults are the choice of the app that writes the config. Verified on loopback stands.

No build tag · [feature](SPECS/FEATURES/019-SNIFF_GUARD/FEATURE.md) · [config](docs-lx/lx-block.md#lxsniff_guard--guard-against-rerouting-by-sni) ([RU](docs-lx/lx-block.ru.md#lxsniff_guard--защита-от-перенаправления-по-sni))

### `chain` builds a multi-hop path from groups without copying them

Entry, middle and exit are each chosen by their own selector or `urltest`, and the path follows the choice. At config load every node on a position ≥ 1, group members included, gets `detour` onto the hop of the position below; a node keeps one instance and one path, so a group on a position checks the path its traffic takes. The MTU alignment and strip filters of `lx.detour` apply to these nodes as to any node under `detour`.

```jsonc
{
  "type": "chain", "tag": "via-three",
  "outbounds": ["selector-in", "selector-mid", "selector-exit"],   // entry → exit
  "rewrite": { "wireguard": { "mtu": 1200 } }                      // merge-patch per node type, positions ≥ 1 only
}
```

The path shows in `detourList` and `GetChainStatus`, every change to a node in `GetOutboundConfig`; the hop tags `<tag>#0`, `<tag>#1`, … give per-layer latency through URLTest. Limits: verified on live stands with real hops; WireGuard on a chain position has not been checked on a device.

`with_lx_chain` · [feature](SPECS/FEATURES/015-CHAIN/FEATURE.md) · [config](docs-lx/lx-chain.md) ([RU](docs-lx/lx-chain.ru.md))

### `urltest` balances across a pool or holds one node until it fails

A group of nodes spreads connections over several servers or holds on to one working server, instead of always picking the node with the lowest delay. `mode: round_robin` with `balancer{…}` spreads connections over a pool of live nodes and keeps each site on its own node; `mode: failover` holds the node until it fails and probes only that node; `least_test`, when a connection through a node fails to open, penalises the node and opens the connection through the next one.

A pool server that goes down between checks drops no requests: the connection opens through a neighbouring slot. A node that refuses connections leaves the pool in under a second; a node that accepts and stays silent is skipped by new connections from the 5th second and struck out in 41 s. Its slot is taken at once by a node from the reserve of `max(⌈pool/2⌉, 1)` live nodes the scheduled check keeps. A node is struck out only when it stays silent on its own probe while a control node answers, so a phone falling asleep, a Wi-Fi switch or an ISP outage leave the pool alone; while nobody answers, the pause between checks grows 15 → 30 → 60 → 120 s and does not drain the battery. A site closed to one node opens through another node of the pool, while the rest of the traffic stays on its nodes.

```jsonc
{ "type": "urltest", "tag": "auto", "outbounds": ["node-a", "node-b", "node-c", "node-d"],
  "mode": "round_robin", "balancer": { "pool": 3 } }   // sticky by ["process","domain"] by default
```

Limits: recovery from failed connections works only in `round_robin`; UDP failures move no sites; a network change the platform does not report (Windows Modern Standby included) can strike out a live node once, and it comes back by itself. The pool state for a UI (`GetPool`) is in a build with `with_lx_command`. The cost per connection — [measurements](SPECS/TASKS/138-URLTEST_RR_SKIP_DEAD_SLOT/IMPLEMENTATION_REPORT.md).

No build tag · verified on a device with a real multi-node pool · [feature](SPECS/FEATURES/007-URLTEST_BALANCE/FEATURE.md) · [guide](docs-lx/lx-urltest.md#pool-self-heal) ([RU](docs-lx/lx-urltest.ru.md#самовосстановление-пула))

### A DNS server group answers from several servers as one

Resolution survives one public resolver going down. A `dns.servers[]` entry of `type: group` queries its members in `stable`, `fastest` or `parallel` mode on a TTL model, and `hosts` answers local names before the network.

```jsonc
{ "type": "group", "tag": "dns-public", "mode": "stable", "servers": ["dns-cf", "dns-google", "dns-quad9"] }
```

No build tag · shipped; a field run is pending · [feature](SPECS/FEATURES/013-DNS_GROUP/FEATURE.md) · [guide](docs-lx/lx-dns-group.md) ([RU](docs-lx/lx-dns-group.ru.md))

### Sniffers recognise WireGuard, OpenVPN, IKE, Tailscale and SIP behind a router

A router routes other devices' VPN tunnels and SIP calls by rule: tunnels through one outbound, calls direct. The names `wireguard`, `openvpn`, `ike`, `tailscale`, `sip` work in the `sniff` action and in `protocol` rules; they match the first packet of a flow and run before the bittorrent sniffer, so WireGuard is not labelled `bittorrent`.

Limits: obfuscated tunnels (AmneziaWG with changed headers) and RTP are not recognised — route those by the server's `ip_cidr`.

No build tag · shipped; a run on a router is pending · [feature](SPECS/FEATURES/016-SNIFF/FEATURE.md) · [guide](docs-lx/lx-sniff.md) ([RU](docs-lx/lx-sniff.ru.md))

### Idle WireGuard tunnels sleep on Android

A profile with a dozen WireGuard or AmneziaWG nodes costs battery and memory only for the nodes in use. Upstream keeps every endpoint alive with its receive buffers and timers; `lx.wg.idle.suspend`, `suspend_reachable` and `teardown` put idle endpoints to sleep in three levels; a wake from the first two costs one round trip, from the third a rebuild. Measured on a device: eight sleeping endpoints freed 134 MB, process memory −31 % ([lx-energy.md](docs-lx/lx-energy.md), [RU](docs-lx/lx-energy.ru.md)).

Limits: built into the AAR only (`with_lx_idle_suspend`); a desktop binary given the key stops at start with an explicit error. An active `least_test` group probes every member and wakes sleepers — use `mode: failover` where that matters.

[feature](SPECS/FEATURES/008-ENERGY/FEATURE.md)

### The `lxd` daemon keeps its control channel through every config change

A core on a router or a server is managed remotely and stays reachable when a new config is broken. `sing-box lxd` runs the core inside a daemon that owns the listening port, so the channel comes up before the core and outlives it.

```bash
sing-box lxd --state-dir ./lxd-state -c config.json
```

- **Apply with rollback.** `POST /admin/apply` validates the candidate; if the start fails, the daemon returns to the last config that worked.
- **One port, two planes.** gRPC for state and logs, REST `/admin/*` for control.
- **mTLS with enrolment.** The daemon is its own CA, prints a one-time invite and knows clients by certificate afterwards.
- **Host telemetry.** CPU per core, memory, temperature, disks, interfaces and an IP → device directory, over the same port.
- **Rule-sets and geo bases are uploaded by name.** `PUT /admin/resources/{name}` refuses a rule-set the core cannot load and keeps the previous copy until a core has started with the new one.
- **The core updates itself on a Linux router.** `sing-box lxd update` downloads the release asset for the platform, verifies it against `SHA256SUMS`, swaps the binary atomically, restarts the service and rolls back if the new one does not come up; on a router with little flash it switches to a low-space mode.
- **Service install.** Automatic on macOS and Windows. On Linux (systemd, OpenWrt / procd) the daemon prints a ready recipe and does not touch the disk; for OpenWrt there is an [installer script](scripts-lx/openwrt/README.md).

Limits: not in the Windows 7 build and not in the AAR.

`with_lxd` · verified on macOS; the OpenWrt scripts are tested in the field · [feature](SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md) · [operator's guide](docs-lx/lxd-daemon.md) ([RU](docs-lx/lxd-daemon.ru.md)) · [VPN on a dedicated SSID on OpenWrt](docs-lx/openwrt-vpn-ssid.md) ([RU](docs-lx/openwrt-vpn-ssid.ru.md))

## Who uses it

The same core and the same config JSON run in different products; mapping subscription links to config fields is each product's own job.

- **Desktop** — [singbox-launcher](https://github.com/Leadaxe/singbox-launcher) bundles the binary and can run it as `lxd`.
- **Android** — [LxBox](https://github.com/Leadaxe/LxBox) embeds `libbox.aar`; F-Droid builds LxBox from source with sing-box-lx as a source library.
- **Routers** — panels and scripts for Keenetic / Netcraze, OpenWrt and Asus Merlin install the core or embed it as a Go module.
- **Other clients** — independent desktop, Android and Apple clients pin a release or fork the core; Hiddify's core documentation names sing-box-lx and utls-lx as the source of its REALITY `key_share`, DNS group modes and browser TLS fingerprints.
- **Distributions** — the ALT Linux `sing-box` package is built with `with_xhttp` and `with_awg`.

The full list with links and what each project takes — **[docs-lx/consumers.md](docs-lx/consumers.md)** ([RU](docs-lx/consumers.ru.md)), as of 2026-11-23.

## For developers: build a client, launcher or router firmware on this core

A team shipping a VPN client, a desktop launcher, router firmware or a management panel gets from this core what matters most during development: [observability](#observability-over-grpc-the-app-sees-what-the-core-is-doing), the live state of the core over gRPC; [network impairment](#network-impairment-without-root-a-controlled-bad-link-inside-the-core) without root to test the product on a bad link before users find the bugs; [a daemon API for a router panel and firmware hooks](#a-daemon-api-for-a-router-web-panel-and-firmware-hooks); [builds of any branch](#builds-of-any-branch-and-config-checks) to try a fix on a device.

### Observability over gRPC: the app sees what the core is doing

A client or a panel shows the live state of the core without scraping logs or opening a Clash API port. One contract runs over two carriers: gRPC of the `lxd` daemon on a desktop, server or router, and the libbox `CommandClient` in the Android and iOS library. The fields mean the same on both, so one UI code path serves a phone and a remote router.

- **Connections.** A stream of the connection table with each connection's path through groups and chains (`detourList`) and the rule that matched it (`GetRules`); closing a connection is one call.
- **DNS.** Every query with its answers and CNAME chain (`SubscribeDNSQueries`).
- **Groups and nodes.** `GetGroups`, the balancer slots with `live` / `dead` / `quarantined` and the moved sites (`GetPool`), chains (`GetChainStatus`), DNS groups, the state of WireGuard / AmneziaWG and Tailscale endpoints by tag.
- **Probes on demand.** The delay of one node (`URLTestOutbound`) and which exit address a node gives (`GetURLViaOutbound`).
- **The core as a whole.** The running config as written and as built (`GetRunningConfig`, `GetEffectiveConfig`, `GetOutboundConfig`), the core's log, memory, goroutines, traffic.

```text
SubscribeConnections{interval: 1s}       → the connection table with each connection's path
SubscribeDNSQueries{includeAnswers: true} → DNS queries with CNAME chains
GetPool{tag}                             → balancer slots and their state
```

The same calls work from a terminal: the fork extends the upstream `sing-box api` command with the `group pool`, `connection-test` and `impair` subcommands.

Limits: the fork's calls are in a build with `with_lx_command`; without the tag they answer `Unimplemented`.

`with_lx_command` · consumed by LxBox · [feature](SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md) · [API](docs-lx/lxd-grpc-api.md) ([RU](docs-lx/lxd-grpc-api.ru.md))

### Network impairment without root: a controlled bad link inside the core

A balancer, failover or a new transport is tested against real servers on a bad network without netem, root or a lab: the `impair` outbound degrades the link inside the core itself, on a desktop, a router or a phone. The config sets what to degrade, how much and when, and `seed` repeats a run exactly. It sits under a proxy node and degrades the road to its server in both directions from one end:

- a bottleneck rate with a queue, delay and jitter, rate swings;
- loss in bursts (Gilbert model) for datagrams; on a TCP carrier a loss stops the stream the way a retransmission does;
- random stalls of the whole link and outages on a schedule, once or in a repeating round, silent or with a connection reset;
- sublinks for part of the traffic: one site, a port, every N-th connection, a share of UDP flows — when `impair` stands over a node, it sees each site;
- profiles `dsl`, `lte-walk`, `wifi-lossy` and your own in `lx.impair_profiles`, one switch of `profile` changes the whole line;
- `seed`: the same run with the same losses, jumps and stalls every time, so a regression shows up as a difference in the build, not in luck.

```jsonc
{ "type": "impair", "tag": "line-a", "seed": 1, "start": "first_use", "schedule_repeat": "2m",
  "link_params": { "profile": "lte-walk" },
  "sublinks": [ { "match": { "domain_suffix": "example.org" }, "link_params": { "loss": "30%" } } ],
  "schedule": [ { "at": "60s", "link_params": { "outage": true, "reset": true } },
                { "at": "70s", "link_params": { "outage": false } } ] }
```

The experiment is driven at runtime, without a core restart that would reset the groups' state. `sing-box api impair set` or `SetImpair` over gRPC change the link mid-run, `ScheduleImpair` replaces the schedule, and observability shows how the pool and the connections respond:

```bash
sing-box api impair set line-a '{"link_params": {"outage": true, "reset": true}}'   # break the link now
sing-box api group pool auto                                                        # what the balancer did
sing-box api impair set line-a '{"link_params": {"profile": "dsl"}}'                 # a steady line from now on
```

`with_lx_impair` is in the release binaries and the AAR, adds 150 KB and does nothing until a config declares `"type": "impair"`; on start it logs a warning that the link is degraded on purpose. Limits: the receive side is degraded after the real network, so the server still sees the real round-trip time.

[guide with recipes for `round_robin` and `failover`](docs-lx/lx-impair.md#recipes-for-balancing-tests) ([RU](docs-lx/lx-impair.ru.md#рецепты-для-испытаний-балансировки)) · [feature](SPECS/FEATURES/020-IMPAIR/FEATURE.md)

### A daemon API for a router web panel, and firmware hooks

A router panel is built on the `lxd` daemon's API, with no agent of its own on the device. Same port and same mTLS as observability:

- **Who is on the network.** `GET /admin/clients-info` turns LAN addresses into devices: name, MAC, SSID, interface; `PUT /admin/clients-info/labels/{key}` keeps the name the operator gave.
- **The state of the machine.** `GET /admin/host` — CPU per core, memory, thermal sensors, disks, file descriptors; `GET /admin/host/interfaces` — counters and rates of every interface; `GET /admin/stats` — the core's traffic and connections.
- **Maintenance buttons.** `daemon.json` defines named commands (`"update": "/usr/bin/sing-box lxd update"`), and `POST /admin/commands/{name}` runs them by name with no input from the client; the output and exit code are read through the same API.
- **Profiles.** `/admin/pprof` serves heap, goroutine, CPU and trace from the remote machine.

Hooks fit the core into firmware. The daemon runs scripts from `<state-dir>/hooks/` on `pre_start`, `post_start`, `post_reload` and `post_stop` and passes the event and reason (`LXD_EVENT`, `LXD_REASON`) and the TUN interface, address and MTU (`LXD_TUN_INTERFACE`, `LXD_TUN_ADDRESS`, `LXD_TUN_MTU`) in the environment. Firmware adds its own firewall rules and routes for the TUN that came up. A failing `pre_start` keeps the core from starting; the outcome of each hook's last run shows in `/admin/status`.

```sh
#!/bin/sh
# <state-dir>/hooks/post_start
nft add rule inet fw4 forward iifname "$LXD_TUN_INTERFACE" accept
```

Limits: the daemon is not in the AAR or the Windows 7 build.

[operator's guide](docs-lx/lxd-daemon.md) ([RU](docs-lx/lxd-daemon.ru.md)) · [hooks](docs-lx/lxd-daemon.md#6a-hooks-the-daemon-runs-the-admins-scripts-when-the-core-changes-state) · [feature](SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md)

### Builds of any branch and config checks

The `lx-build.yml` workflow builds a binary, Linux musl, the Android AAR or the Apple xcframework from any branch or tag of this repository, so a branch with a fix goes onto a device before the release:

```bash
gh workflow run lx-build.yml -f target=android-aar -f branch=<branch>
make -f Makefile.lx lx-check      # build, then sing-box check every config in lx-test/config/
```

## How the fork stays close to upstream

```
upstream/stable  ──merge──►  lx  =  upstream  +  // lx seams  +  lx-owned files  +  4 fork submodules
                                     │
                                     └─►  tag vX.Y.Z-lx.N  ──►  lx-release.yml  ──►  GitHub Release
```

- **Every `upstream/stable` release is merged by hand, never rebased.** The `lx` branch is never force-pushed; drift is measured against `upstream/stable` only.
- **The fork's code is separable.** New code lives in its own files behind build tags; an edit in an upstream file is a block marked `// lx`, and `grep -rn "lx:begin"` lists them all. The Go module path stays `github.com/sagernet/sing-box`.
- **Four fork submodules, each its upstream plus a few commits:** [wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx) (AmneziaWG runtime), [sing-tun-lx](https://github.com/Leadaxe/sing-tun-lx) (accept-loop self-heal), [gvisor-lx](https://github.com/Leadaxe/gvisor-lx) (handshake nil-guard), [utls-lx](https://github.com/Leadaxe/utls-lx) (Firefox 148 and Safari 26.3 presets).
- **Fixes carry an expiry.** Bugs found in real use are fixed here with a test; every patch in the [HOTFIXES registry](SPECS/FEATURES/004-HOTFIXES/FEATURE.md) names the condition under which it is removed.
- **Releases.** Tags `vX.Y.Z-lx.N` are stable, `-rc.N` / `-alpha.N` / `-beta.N` are pre-releases. Procedure — [release runbook](docs-lx/lx-release-runbook.md) ([RU](docs-lx/lx-release-runbook.ru.md)); notes — [`docs-lx/releases/`](docs-lx/releases/); engineering log — [lx-changelog.md](docs-lx/lx-changelog.md).
- **Rules and process.** [CONSTITUTION](SPECS/CONSTITUTION.md) holds the rules every feature lives by; [`SPECS/FEATURES`](SPECS/FEATURES/README.md) describes each feature's current state, [`SPECS/TASKS`](SPECS/README.md) each unit of work. The path of a feature and the PR requirements — [CONTRIBUTING.md](CONTRIBUTING.md).

Scope: client-first. The fork adds outbounds and endpoints; the one inbound is `trusttunnel`.

## Known issues and common questions

### Xray Mux.Cool is not sing-box `multiplex`

Xray's `mux` (Mux.Cool) and sing-box's `multiplex` block are different protocols with the same idea. `multiplex` speaks sing-mux (`smux` / `yamux` / `h2mux`) and works only when the server is sing-box with multiplex enabled; an Xray server does not understand it and the node stops passing traffic. So `mux` from an Xray link or subscription must not be translated into `multiplex` — drop it. Nothing is lost for UDP: a `vless` outbound already packs UDP as XUDP by default, see `packet_encoding` in the [VLESS outbound](https://sing-box.sagernet.org/configuration/outbound/vless/) reference.

### REALITY: `reality verification failed` after the server updated Xray

Use the `chrome`, `firefox` or `safari` fingerprint and leave `key_share` empty — see [REALITY](#reality-works-with-current-xray-servers-including-post-quantum-x25519mlkem768). If the handshake hangs with no error, the network is losing the second segment of the ClientHello: add `record_fragment` or a `detour`. Symptom table — [xray-protocols-explained.md §2.7](docs-lx/xray-protocols-explained.md#27-typical-failures) ([RU](docs-lx/xray-protocols-explained.ru.md#27-типичные-отказы)).

## Documentation

| | |
|---|---|
| Config overview | [docs-lx/lx-config.md](docs-lx/lx-config.md) ([RU](docs-lx/lx-config.ru.md)) — every field of every feature, with examples |
| Protocols & transports | [docs-lx/protocols-transports.md](docs-lx/protocols-transports.md) ([RU](docs-lx/protocols-transports.ru.md)) — XHTTP, AmneziaWG, MASQUE, REALITY, VLESS `encryption`, TrustTunnel in depth |
| How the Xray protocols work | [docs-lx/xray-protocols-explained.md](docs-lx/xray-protocols-explained.md) ([RU](docs-lx/xray-protocols-explained.ru.md)) — REALITY, Vision, XHTTP, VLESS `encryption`: mechanics, Xray ↔ sing-box-lx examples |
| How AmneziaWG works | [docs-lx/amneziawg-explained.md](docs-lx/amneziawg-explained.md) ([RU](docs-lx/amneziawg-explained.ru.md)) — three obfuscation layers, the MTU budget, `awg.conf` ↔ endpoint |
| How TrustTunnel works | [docs-lx/trusttunnel-protocols-explained.md](docs-lx/trusttunnel-protocols-explained.md) ([RU](docs-lx/trusttunnel-protocols-explained.ru.md)) — the wire format, the session model, outbound and inbound |
| Energy guide | [docs-lx/lx-energy.md](docs-lx/lx-energy.md) ([RU](docs-lx/lx-energy.ru.md)) — sleep levels, `mode: failover`, a mobile config |
| Sniffers | [docs-lx/lx-sniff.md](docs-lx/lx-sniff.md) ([RU](docs-lx/lx-sniff.ru.md)) |
| `lxd` operator's guide | [docs-lx/lxd-daemon.md](docs-lx/lxd-daemon.md) ([RU](docs-lx/lxd-daemon.ru.md)) |
| Observability API | [docs-lx/lxd-grpc-api.md](docs-lx/lxd-grpc-api.md) ([RU](docs-lx/lxd-grpc-api.ru.md)) — the contract clients speak, daemon and Android AAR alike |
| OpenWrt walkthrough | [docs-lx/openwrt-vpn-ssid.md](docs-lx/openwrt-vpn-ssid.md) ([RU](docs-lx/openwrt-vpn-ssid.ru.md)) · [installer scripts](scripts-lx/openwrt/README.md) |
| Who uses the core | [docs-lx/consumers.md](docs-lx/consumers.md) ([RU](docs-lx/consumers.ru.md)) |
| Releases | [release notes](docs-lx/releases/) · [lx-changelog.md](docs-lx/lx-changelog.md) · [release runbook](docs-lx/lx-release-runbook.md) ([RU](docs-lx/lx-release-runbook.ru.md)) |
| Reference cores | [docs-lx/lx-reference-cores.md](docs-lx/lx-reference-cores.md) ([RU](docs-lx/lx-reference-cores.ru.md)) — where to look for wire-protocol answers |
| Spec Kit | [SPECS/FEATURES](SPECS/FEATURES/README.md) · [SPECS/TASKS](SPECS/README.md) · [CONSTITUTION](SPECS/CONSTITUTION.md) · [CONTRIBUTING](CONTRIBUTING.md) |
| sing-box | [SagerNet/sing-box](https://github.com/SagerNet/sing-box) · [documentation](https://sing-box.sagernet.org/) — the platform this core is built on: protocols, routing, the TUN stack, `libbox` |
| Protocol origins | [XTLS/Xray-core](https://github.com/XTLS/Xray-core) (XHTTP, REALITY, VLESS encryption) · [amnezia-vpn/amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) · [Cloudflare WARP / MASQUE](https://developers.cloudflare.com/warp-client/) |

## Repository map

| Path | Purpose |
|------|---------|
| `Makefile.lx` | build with the lx tag set and the `-lx` version: `lx-build`, `lx-check`, `lx-print-tags`, `lx-proto` |
| `.github/workflows/lx-ci.yml`, `lx-release.yml`, `lx-build.yml` | CI, release on `v*-lx.*` tags, on-demand builder for any branch |
| `SPECS/`, `docs-lx/` | Spec Kit and the fork's documentation (EN + RU) |
| `lx-test/`, `scripts-lx/openwrt/` | sample configs and live stands; the router installer for `lxd` |
| `transport/v2rayxhttp/`, `option/v2ray_xhttp.go` | XHTTP |
| `transport/wireguard/device_awg.go`, `option/wireguard_awg.go`, `submodules/wireguard-go` | AmneziaWG |
| `protocol/masque/`, `option/masque_lx.go` | MASQUE / CONNECT-IP |
| `protocol/chain/`, `option/chain_lx.go` | `chain` |
| `common/tls/` (`*_lx*`), `submodules/utls` | REALITY key share, fragmentation, fingerprint presets |
| `common/sniff/*_lx.go` | sniffers |
| `dns/transport/group/`, `common/dnstrack/` | DNS group and the query trace behind `SubscribeDNSQueries` |
| `experimental/libbox/`, `daemon/` (`lx:` seams), `lxd/` | `CommandClient` extensions and the daemon |
| `submodules/sing-tun`, `submodules/gvisor` | fork submodules for the TUN stack |

## Support the project

sing-box-lx is free and has no paid features. Ways to help, with and without money — **[docs-lx/donate.md](docs-lx/donate.md)** ([RU](docs-lx/donate.ru.md)).

## License

**GPL-3.0**, inherited from sing-box. The fork's edits are marked `// lx` and distributed under the same license. This is an unofficial fork, not affiliated with SagerNet.
