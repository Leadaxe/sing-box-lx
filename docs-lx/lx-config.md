# sing-box-lx — configuration of the downstream features

> 🌐 Русская версия: **[lx-config.ru.md](lx-config.ru.md)**.

> 🧭 **Where to look.** The fork's documentation has three levels, by the reader's question:
> [lx-config](lx-config.md) — what the fork has and how to enable it;
> [protocols-transports](protocols-transports.md) — every field, type, default, error text;
> [xray-protocols-explained](xray-protocols-explained.md), [amneziawg-explained](amneziawg-explained.md) and [masque-explained](masque-explained.md) —
> how it works, why, how the fork does it and how it differs from vanilla.

## Table of contents

- [Features and build tags](#features-and-build-tags)
- [0. Every field at a glance (exhaustive example)](#0-every-field-at-a-glance-exhaustive-example)
- [1. XHTTP transport](#1-xhttp-transport)
  - [Example — VLESS + XHTTP + Reality](#example--vless--xhttp--reality)
- [2. AmneziaWG 2.0/3.x (AWG2, AWG3)](#2-amneziawg-203x-awg2-awg3)
  - [Example — AmneziaWG 3.1 endpoint (Amnezia `amnezia-awg2` export)](#example--amneziawg-31-endpoint-amnezia-amnezia-awg2-export)
  - [Example — AmneziaWG 2.0 endpoint](#example--amneziawg-20-endpoint)
- [3. urltest node selection modes](#3-urltest-node-selection-modes)
- [4. MASQUE outbound — Cloudflare WARP](#4-masque-outbound--cloudflare-warp)
- [5. DNS server group](#5-dns-server-group)
- [6. VLESS `encryption` — post-quantum layer](#6-vless-encryption--post-quantum-layer)
  - [Field (on a `vless` outbound)](#field-on-a-vless-outbound)
  - [Example](#example)
- [7. REALITY `key_share` — hybrid or classical ClientHello](#7-reality-key_share--hybrid-or-classical-clienthello)
- [8. Observability (CommandClient extensions)](#8-observability-commandclient-extensions)
- [9. ClientHello under `detour` — no default](#9-clienthello-under-detour--no-default)
- [10. `chain` outbound — a virtual multi-hop path of groups and nodes](#10-chain-outbound--a-virtual-multi-hop-path-of-groups-and-nodes)
- [11. Protocol sniffers for LAN traffic](#11-protocol-sniffers-for-lan-traffic)
- [12. Validate & build](#12-validate--build)
- [13. The `lx` root block](lx-block.md)
- [14. Tailscale: turning Taildrop off](#14-tailscale-turning-taildrop-off)

---

## Features and build tags

`sing-box-lx` is upstream [sing-box](https://github.com/SagerNet/sing-box) plus a small set of client-side features, each gated behind a build tag:

| Feature | Build tag | Where it lives in config | Included in |
|---------|-----------|--------------------------|-------------|
| **XHTTP** transport (Xray-compatible) | `with_xhttp` | `transport.type: "xhttp"` on a VLESS / VMess / Trojan outbound | desktop + mobile |
| **AmneziaWG 2.0/3.x** (AWG2, AWG3) | `with_awg` | extra fields on a `wireguard` endpoint | desktop + mobile |
| **MASQUE** outbound (CONNECT-IP / WARP) | `with_quic`+`with_gvisor` | `outbounds[].type: "masque"` | desktop + mobile |
| **TrustTunnel** outbound and inbound | `with_trusttunnel` (+ `with_quic` for HTTP/3) | `outbounds[].type` / `inbounds[].type: "trusttunnel"`; fields — [protocols-transports.md §6](protocols-transports.md#6-trusttunnel-outbound-and-inbound), the global `lx.trusttunnel.idle_timeout` — [lx-block.md](lx-block.md#lxtrusttunnel--trusttunnel-outbounds) | desktop + mobile |
| **Idle-suspend** | `with_lx_idle_suspend` | `lx.wg.idle.suspend` (+ `suspend_reachable`, `teardown`) | mobile only (AAR) |
| **Root `lx` block** | — (always parsed) | `lx.wg`, `lx.masque`, `lx.trusttunnel`, `lx.mtu_align`, `lx.sniff_guard` — the fork's global knobs, [lx-block.md](lx-block.md) | desktop + mobile |
| **DNS server group** | — (always built) | `dns.servers[].type: "group"` | desktop + mobile |
| **VLESS `encryption`** | — (always built) | `encryption` on a `vless` outbound | desktop + mobile |
| **`lxd` daemon** | `with_lxd` | not a config key — the `sing-box lxd` subcommand + `<state-dir>/daemon.json`; see [lxd-daemon.md](lxd-daemon.md) | desktop / server (not Win7, not AAR) |
| **`chain` outbound** | `with_lx_chain` | `outbounds[].type: "chain"` — a multi-hop path of groups and nodes | desktop + mobile |

`make -f Makefile.lx lx-build` builds the desktop/CLI binary: the output is `sing-box` with version `…-lx.N`.
The build includes `with_xhttp` and `with_awg` (plus `with_lx_command` and `with_lxd`), but not `with_lx_idle_suspend`.
Without a tag the feature is absent: an `xhttp` transport or an AWG field is rejected at load time with an explicit error, with no silent downgrade.

The `with_lx_idle_suspend` tag goes only into the mobile AAR (`cmd/internal/build_libbox`), not
into the desktop `LX_TAGS`. It suspends idle WireGuard/AmneziaWG endpoints and frees their
receive buffers: on a device, 8 suspended endpoints freed 134 MB. The `lx.wg.idle` keys and the
desktop binary refusing them are in [lx-block.md](lx-block.md#lxwg--wireguard--amneziawg-endpoints); the energy
model and the recommended mobile configuration are in [lx-energy.md](lx-energy.md).

> ⚠️ All keys/UUIDs below are placeholders. Never commit real private keys / pre-shared keys to a repository.

---

## 0. Every field at a glance (exhaustive example)

One config that carries every field of the fork's features with comments: the default and
the allowed values of each. It is a reference for those who write a config generator, not a
working setup: some fields are mutually exclusive or server-only. The whole example is in
[lx-config-example.md](lx-config-example.md).

---

## 1. XHTTP transport

XHTTP ("splithttp"/"xhttp" in Xray) is a v2ray transport that tunnels the proxy over plain HTTP/2 requests. It attaches to VLESS / VMess / Trojan via the shared `transport` block and composes with TLS, including Reality. XHTTP is incompatible with XTLS-Vision; that is a protocol limitation, not the fork's. The default wire shape is byte-identical to the v1 client verified live. Every v2 field is opt-in: session/seq placement, uplink obfs, the `x_padding_*` family, `xmux` connection reuse.

A minimal `transport` block is just `"type": "xhttp"` (mode `auto`). The [example below](#example--vless--xhttp--reality) adds a Reality node with `stream-one`.

> 📖 The full field reference is in
> [protocols-transports.md §1](protocols-transports.md#1-xhttp-transport)
> ([RU](protocols-transports.ru.md#1-xhttp-транспорт)): all 26 XHTTP keys with their defaults,
> the `xmux` pool semantics, range value forms and a troubleshooting table.

### Example — VLESS + XHTTP + Reality

```jsonc
{
  "type": "vless",
  "tag": "xhttp-out",
  "server": "example.com",
  "server_port": 443,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "tls": {
    "enabled": true,
    "server_name": "example.com",
    "utls": { "enabled": true, "fingerprint": "chrome" },
    "reality": { "enabled": true, "public_key": "<reality-public-key-base64>", "short_id": "0123abcd" }
  },
  "transport": {
    "type": "xhttp",
    "mode": "stream-one",
    "host": "example.com",
    "path": "/xhttp",
    "x_padding_bytes": "100-1000"
  }
}
```

---

## 2. AmneziaWG 2.0/3.x (AWG2, AWG3)

AWG is WireGuard + DPI-evasion obfuscation. It is configured as a normal sing-box `wireguard` endpoint with extra fields. With `with_awg` these are pushed to the device. A config without any AWG field is a plain WireGuard endpoint, byte-identical to upstream behavior.

AWG2 = AWG1 fields plus the CPS packets `I1`–`I5`. Both client and server must run AmneziaWG with matching parameters: the I-packets are configuration and are not negotiated. The first decoy is easier to set with the WireSock-style `id`/`ip`/`ib` sugar, which generates `i1` for you. The sugar is described in the [full reference](protocols-transports.md#25-masquerade-sugar-id--ip--ib).

AWG3 is amneziawg-go v3.0/v3.1, Amnezia's `amnezia-awg2` container with `protocol_version` 3.x. It adds header protection, content padding, random trailers, disabled cookies, ranged timing overrides and a ranged `persistent_keepalive_interval`. The header protection key `header_protection_key` is server-side and must match the server's. Like the AWG2 ones, all AWG3 fields sit at the endpoint root — [reference §2.10](protocols-transports.md#210-awg-3x-header-protection-padding-trailers-timings).

The AWG fields sit at the endpoint root, none on a peer, as in the `[Interface]` section
of an `awg-quick` `.conf`: junk (`jc`/`jmin`/`jmax`), handshake padding (`s1`–`s4`),
magic headers (`h1`–`h4`, single value or `"min-max"` range), and CPS decoys (`i1`–`i5`).
An AWG endpoint needs a lower `mtu` than plain WireGuard because `s4` pads every data
packet. When `s4` is set and `mtu` is not, the core uses `1280`.

> 📖 The full field reference is in
> [protocols-transports.md §2](protocols-transports.md#2-amneziawg-203x-awg2-awg3)
> ([RU](protocols-transports.ru.md#2-amneziawg-203x-awg2-awg3)): every junk/signature/magic/CPS
> field with its type and default, the CPS tag format, the `id`/`ip`/`ib` masquerade sugar (four
> profiles, which to pick, what reaches the wire), the MTU budget math, the `awg.conf` 1:1 mapping
> and the verbatim validation errors.

### Example — AmneziaWG 3.1 endpoint (Amnezia `amnezia-awg2` export)

```jsonc
{
  "type": "wireguard", "tag": "awg3-out", "system": false, "mtu": 1376,
  "address": ["10.8.1.7/32"],
  "private_key": "<client-private-key-base64>",
  "jc": 4, "jmin": 10, "jmax": 50,
  "s1": 55, "s2": 42, "s3": 40, "s4": 12,      // all >= 12 for header protection
  "h1": 1, "h2": 2, "h3": 3, "h4": 4,
  "header_protection_key": "<HeaderProtectionKey-base64>",
  "content_padding_addition": "10-100",
  "rekey_after_time": "100-120", "rekey_timeout": "3-7", "reject_after_time": "150-180",
  "keepalive_timeout": "5-15", "max_handshake_attempts": "15-20",
  "random_trailers": true, "disable_cookies": true,
  "peers": [ { "address": "77.239.123.44", "port": 30565,
    "public_key": "<server-public-key-base64>", "pre_shared_key": "<preshared-key-base64>",
    "allowed_ips": ["0.0.0.0/0", "::/0"], "persistent_keepalive_interval": "25-35" } ]
}
```

### Example — AmneziaWG 2.0 endpoint

```jsonc
{
  "type": "wireguard",
  "tag": "awg-out",
  "system": false,
  "mtu": 1280,
  "address": ["10.0.0.2/32"],
  "private_key": "<client-private-key-base64>",

  "jc": 10, "jmin": 50, "jmax": 100,
  "s1": 20, "s2": 20, "s3": 60, "s4": 60,
  // single values (AWG 1.x style) — or ranged AWG 2.0 headers, e.g.
  // "h1": "43613244-384550127", "h2": "826869626-2105069164",
  // "h3": "2124774725-2141151992", "h4": "2144594503-2146278491",
  "h1": 1234567890, "h2": 1234567891, "h3": 1234567892, "h4": 1234567893,
  "i1": "<b 0x000100002112a442><r 12>",
  "i2": "<b 0x010100002112a442><r 12>",
  "i3": "<r 24>",

  "peers": [
    {
      "address": "server.example.com",
      "port": 51821,
      "public_key": "<server-public-key-base64>",
      "pre_shared_key": "<preshared-key-base64>",
      "allowed_ips": ["0.0.0.0/0", "::/0"],
      "persistent_keepalive_interval": 25
    }
  ]
}
```

The [full reference](protocols-transports.md#2-amneziawg-203x-awg2-awg3) covers three topics in detail:

- the `id`/`ip`/`ib` masquerade sugar — four decoy profiles: `quic`/`dns`/`stun`/`sip`;
- the MTU budget — why `s4` requires a lower MTU, the `sendmsg: message too long` symptom,
  the auto-`1280` default, `udp_fragment` for nested tunnels;
- the `awg.conf` 1:1 mapping.

The runtime is `Leadaxe/wireguard-go`: sagernet/wireguard-go with AmneziaWG obfuscation, wired in as the `submodules/wireguard-go` submodule. How the feature works is described in the [AWG feature](../SPECS/FEATURES/003-AWG/FEATURE.md).

---

## 3. urltest node selection modes

Upstream `urltest` always selects the lowest-delay node. sing-box-lx adds two more modes:

| `mode` | Selection | Switches | Checked each `interval` |
|---|---|---|---|
| `least_test` (default) | the fastest node | when another one is faster by `tolerance` or a connection through the current one fails to open | every node |
| `round_robin` | a pool of live nodes | connections are spread over the pool | only the pool |
| `failover` | the fastest node at selection time | only when the current one fails or gets twice as slow as the others; the next one is again the fastest | only the current node |

The `url`, `interval`, `idle_timeout` and `interrupt_exist_connections` fields work as upstream.
The `mode` and `balancer` fields work in every build; the pool state for a UI (`GetPool`,
[§8](#8-observability-commandclient-extensions)) is in a build with `with_lx_command`.

The fields, how each mode works, the `round_robin` pool self-heal and examples are in
[lx-urltest.md](lx-urltest.md).

---

## 4. MASQUE outbound — Cloudflare WARP

A `masque` outbound connects to Cloudflare WARP and other CONNECT-IP (RFC 9484) servers: whole IP
packets go over HTTP/3 or HTTP/2, and the core builds its own userspace stack on the tunnel, as
for a WireGuard endpoint. It shares only the word with the AWG `id/ip/ib` *masquerade* sugar in
§2. It needs the `with_quic` and `with_gvisor` tags, both in `LX_TAGS`. Keys are taken ready
from the config: the client registers the device with WARP, not the core.

The required fields are `server`/`server_port`, the `private_key`/`public_key` pair and at least
one of `ip`/`ipv6`: the client's address inside the tunnel, not the exit IP. A top-level `dns`
block is required: the outbound resolves domains through the DNS router. Every other field has a
default:

| Field | Default |
|-------|---------|
| `profile` | `cloudflare` |
| `vhttp` | `auto` |
| `tls.server_name` | `www.cloudflare.com` |
| `mtu` | `1280` |
| `idle_timeout` | off, or the global `lx.masque.idle_timeout` ([lx-block.md](lx-block.md)); the node's own key wins over the global one, `"0"` included |
| `keep_alive_period` | `30s` |
| `network_list` | `tcp+udp` |

```jsonc
{
  "type": "masque",
  "tag": "warp",
  "server": "162.159.198.2",
  "server_port": 443,
  "private_key": "<base64 DER EC private key>",
  "public_key":  "<base64 DER PKIX public key>",
  "ip":   "172.16.0.2/32",
  "ipv6": "2606:4700:110:...::/128"
}
```

- The default SNI is `www.cloudflare.com`, not the endpoint name: DPI filters a MASQUE endpoint
  by its name in the ClientHello, and the server is checked by `public_key`.
- `vhttp: auto` tries h3 first and falls back to h2 (TCP:443) if QUIC is not up within 3 s, and
  remembers what worked. It is for networks and `detour` hops that silently swallow QUIC. The
  `standard` profile works over h3 only and requires `uri`.
- Nodes above masque (through `detour`, a group or in a `chain`) get their packet size fitted to
  `mtu` on their own ([`lx.mtu_align`](lx-block.md#lxmtu_align--path-mtu--packet-size-alignment)): WARP
  drops inner IPv6 fragments. The outer QUIC to the server starts with an `mtu + 51` byte packet.
- The HTTP version is set by `vhttp`; a `network` field in a `masque` node is a load error.

Verified on a device on Wi-Fi and LTE, over `h3` and `h2`, with sleep and recovery after a drop.

📖 How it works and why — [masque-explained.md](masque-explained.md). Every field, the profiles, examples and common footguns are in
[protocols-transports.md §3](protocols-transports.md#3-masque-outbound-connect-ip--warp)
([RU](protocols-transports.ru.md#3-masque-outbound-connect-ip--warp)).

---

## 5. DNS server group

The `group` type in `dns.servers[]` puts several DNS servers behind one tag: if one server does
not answer, the query goes to the others. Modes: `stable` holds the current server, `fastest`
takes the one that most often answered first, `parallel` asks everyone at once. The `hosts` key
answers local names before the network is asked. No build tag is needed.

```jsonc
{ "type": "group", "tag": "public", "servers": ["google", "cloudflare"], "mode": "stable" }
```

The fields, how the group picks a server, `hosts` and examples are in [lx-dns-group.md](lx-dns-group.md).

## 6. VLESS `encryption` — post-quantum layer

A flat `encryption` field on a `vless` outbound enables the `mlkem768x25519plus` handshake
inside VLESS. The layer sits above the transport and TLS and below the VLESS client. It is
independent of REALITY's key exchange, which is a different layer. Servers that mandate it
(Xray `decryption` configured) silently drop plain VLESS. The transport comes up: WS answers
`101`, gRPC answers SETTINGS. Then the peer tears the connection down without a line in the
core log; this is the symptom the field cures. The fork has only the client half: it is a
client-focused fork, and the server half is not ported. The field is always built, with no
build tag.

### Field (on a `vless` outbound)

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `encryption` | string | `""` | `""`/`"none"` = layer off (upstream behavior, byte for byte). Otherwise a spec string; `check` and start validate it, and the error names the exact segment |

Spec-string grammar (dot-separated):

```
mlkem768x25519plus.<native|xorpub|random>.<0rtt|1rtt>[.<padding>…].<key>[.<key>…]
```

- **appearance** — how the layer looks on the wire: `native` (AEAD headers shaped
  like TLSv1.3), `xorpub` (XOR-ed by public key), `random`.
- **rtt** — `0rtt` or `1rtt`.
- **padding** — optional short blocks like `100-111-1111`. A segment shorter than 20 chars
  before the first key is read as padding.
- **key** — base64url of an X25519 (32-byte) or ML-KEM-768 (1184-byte) public
  key; several keys may be listed. A working ML-KEM-768 key is ~1579 chars; a
  much shorter one is a truncated key, not a different format.

### Example

```jsonc
{
  "type": "vless",
  "tag": "pq-node",
  "server": "example.com",
  "server_port": 443,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "encryption": "mlkem768x25519plus.native.0rtt.<base64url ML-KEM-768 key>",
  "transport": { "type": "ws", "path": "/ws" }
}
```

The field shipped in `v1.14.0-lx.18` and is device-verified on the subscription that prompted
it: 10 nodes that were dead without it started working (6 of 8 WS, 4 of 4 gRPC), and no other
transport group changed. The form seen in the field is `native.0rtt`. `1rtt` with padding and
`xorpub`/`random` parse and build but have not met a live server yet.

> ⚠️ In a subscription the value arrives as `settings.vnext[0].users[0].encryption`. On the
> sing-box outbound it is a flat `encryption` field beside `uuid`. A config builder that drops
> it leaves the core with nothing to act on.

---

## 7. REALITY `key_share` — hybrid or classical ClientHello

`key_share` is a string on `tls.reality`, set per node:

```json
"reality": { "enabled": true, "public_key": "…", "short_id": "0123abcd", "key_share": "classical" }
```

| Value | ClientHello | Works against |
|---|---|---|
| `""` (unset) | Whatever the fingerprint carries: `chrome` / `firefox` / `safari` send the `X25519MLKEM768` hybrid share, `edge` / `ios` / `android` / `360` / `qq` send X25519 only. The core changes nothing. | any Xray if the fingerprint carries the hybrid share; otherwise only Xray < v26.9.8 |
| `"classical"` | `X25519MLKEM768` removed from `key_share` and `supported_groups`. The same hello as upstream, ~1.2 KB shorter (`chrome`: 594 B instead of 1720 B, one TCP segment instead of two). | Xray < v26.9.8 only. Newer servers silently reject a hello without the hybrid share (`reality verification failed`). |
| `"hybrid"` | Requires the hybrid share. On a fingerprint that has none the handshake fails at once with `reality key_share "hybrid": fingerprint Edge 85 carries no X25519MLKEM768 key share`. Without this value the server would reject silently, and that looks the same as a wrong key. On `chrome` / `firefox` / `safari` it changes nothing. | as `""` |

Any other value is a config error at load time: a typo must not silently become the default.

Some networks drop the two-segment hybrid first flight while the one-segment classical one
passes (LxBox #142). Xray's own client hits the same problem (XTLS#6256). Newer Xray servers
require the hybrid share. The core cannot pick for the user between "the server rejects it"
and "the network loses it", so the choice is set per node. `classical` reduces post-quantum
protection and only fits older servers. For a new server on such a network the remaining
options are `record_fragment` ([§9](#9-clienthello-under-detour--no-default)) and a `detour`.

---

## 8. Observability (CommandClient extensions)

The fork adds methods to libbox's `CommandClient` through which a client sees groups, the pool,
chains, rules, the running config and DNS queries without scraping logs. This is an API, not
config keys: the methods are in a build with `with_lx_command` (part of
`make -f Makefile.lx lx-build`), and absent without the tag.

| Method | What it returns or does |
|--------|-------------------------|
| `URLTestOutbound(tag, link, timeout)` | an on-demand delay test of one node |
| `GetRules()` | the route and DNS rule table |
| `GetGroups()` | outbound groups and their choice |
| `GetOutbounds()` | a flat list of outbounds and endpoints; for WG/AWG — state and idle time |
| `GetPool(groupTag)` | the `round_robin` pool slots with state and counters |
| `GetPoolState(groupTag)` | the same plus moved keys, address problems and the freeze |
| `GetDNSGroups()` | the live state of every `group` DNS server |
| `GetRunningConfig()` | the canonical JSON of the running config |
| `SubscribeDNSQueries(includeAnswers, handler)` | a live stream of DNS queries |
| `GetChains()` | the state of every `chain` outbound by position |
| `SetEndpointEnabled(tag, enabled)` | turns a WG/AWG endpoint off and on at runtime |

Connections in the stream carry a `detourList` field — the path through `detour` to the final
outbound.

```sh
sing-box api group pool auto     # the round_robin pool of group auto (a build with with_lx_command)
```

The fields of each method and their values are in [lx-observability.md](lx-observability.md).

---

## 9. ClientHello under `detour` — no default

> ⚠️ Without the `lx.sniff_guard` block the core splits the ClientHello as upstream does:
> only where the node sets `fragment` or `record_fragment`, under `detour` too. In earlier
> releases a TLS-over-TCP outbound (VLESS, trojan, vmess, anytls, shadowtls, http, masque
> `h2`, …) dialling through `detour` split its ClientHello into TLS records by default
> (`record_fragment`). A config without the block loads with no error, and TLS nodes behind
> relays that reroute connections by SNI stop connecting.

The split protects against a relay that reads the SNI out of the ClientHello and sends the
connection to that name instead of the node's destination. This is what Xray `sniffing` with
`destOverride` and without `routeOnly` does. A node whose SNI is a disguise (WARP with a
foreign name, REALITY) then gets a connection that goes elsewhere: the handshake hangs and
dies as `tls handshake: EOF` after 12–17 s, or a foreign key arrives. Xray's TLS sniffer does
not parse a ClientHello split into TLS records. Measured through a sniffing leg: no split ❌
fail in 12 s; `fragment` ✅ 0.6 s; `record_fragment` ✅ 0.1 s.

The [`lx.sniff_guard`](lx-block.md#lxsniff_guard--guard-against-rerouting-by-sni) block (§13) turns the
protection on. The minimal block:

```json
"lx": { "sniff_guard": { "tls": { "method": "record_fragment" } } }
```

With it, a path through any relay except `ssh` and `socks` starts with the split, and the
direct path without it. A path through `ssh` or `socks` starts without the split. The split is
turned on there once a node on that path failed without it and then passed with it. To start
every relay with the split, as the old default did, add
`"detour_type": {"ssh": "suspect", "socks": "suspect"}`. The map is laid over the defaults, so
only the entries that change need listing. Nodes whose SNI matches `server` are not guarded by
default (`nodes.sni_mismatch`). The old default split theirs too; to split them as well, add
`"nodes": {"sni_mismatch": false}`. Unlike the old default, the block learns from connection
outcomes and can drop the split on a path where it gets in the way.

Node-level upstream keys:

- **`fragment` / `record_fragment`** on a node work as upstream, with or without the block.
  The block overrides them only with `"override": true`.
- **`"record_fragment": false`** — the node asks for the ClientHello in one record. Under the
  block without `override` such a node goes unsplit on every path.
- **REALITY nodes** — `fragment` / `record_fragment` work on them too. In upstream the REALITY
  client builds its uTLS connection on the bare socket, and these keys are silently ignored
  there. The hybrid ClientHello is 1.5–1.9 KB, which is two TCP segments. Whether a split
  helps on a network that drops that first flight is a property of that network. The other
  option is `key_share` ([§7](#7-reality-key_share--hybrid-or-classical-clienthello)).
- Only the handshake is split; the traffic after it is untouched.

---

## 10. `chain` outbound — a virtual multi-hop path of groups and nodes

The `chain` outbound builds a multi-hop path from existing nodes and groups. Each position is chosen
by its own group with all of its logic, and the chain follows the choice. A node at position ≥ 1
works through its link — an instance that dials its own server through the previous position.
Links are created on first use and removed after `idle_timeout` without connections. The links'
MTU is fitted on its own, and one-sided anti-DPI tricks are stripped on inner positions. It needs
the `with_lx_chain` build tag (in the desktop `LX_TAGS` and in the AAR).

| Key | Default | What it sets |
|-----|---------|--------------|
| `outbounds` | required | position tags in packet order, from the client to the exit; at least two, all different |
| `idle_timeout` | `5m` | after how long a link without live connections is removed; `0` — live until shutdown |
| `strip_evasion` | `true` | strip one-sided anti-DPI tricks from links at positions ≥ 1 |
| `strip` | `{}` | changes to the strip list: `false` — keep, `true` — strip as well |
| `rewrite` | `{}` | a merge-patch to the links' config by node type at positions ≥ 1 |

```jsonc
{
  "type": "chain",
  "tag": "via-three",
  "outbounds": ["selector-in", "selector-mid", "selector-exit"],
  "rewrite": { "wireguard": { "mtu": 1200 } }
}
```

The strip catalogue, link MTU, observability and limits are in [lx-chain.md](lx-chain.md).

---

## 11. Protocol sniffers for LAN traffic

The fork adds protocol names for the `sniffer` list of a `sniff` action and the `protocol`
rule matcher: `wireguard`, `openvpn`, `ike`, `tailscale`, `sip`. `sip` also sets `domain` from
the Request-URI. The sniffers recognise VPN tunnels and calls from other devices behind a
router by the shape of the first packet. They sit before upstream's uTP sniffer, because that
one labels plain WireGuard as `bittorrent`. Only the first packet of a flow counts, so junk
and decoys are not seen through. Order, limits and a router example are in
[lx-sniff.md](lx-sniff.md).

## 12. Validate & build

```sh
git clone --recurse-submodules <repo>           # with_awg needs the submodule
make -f Makefile.lx lx-build                     # builds ./sing-box with both features
./sing-box check -c lx-test/config/xhttp_reality.json
./sing-box check -c lx-test/config/awg2_basic.json
./sing-box check -c lx-test/config/awg3_full.json     # AmneziaWG 3.1 field set
./sing-box check -c lx-test/config/lx_block.json      # the root lx block, every key
make -f Makefile.lx lx-check                     # build + check every lx-test/config/*.json

# Android (optional): libbox.aar with with_xhttp+with_awg baked in (needs NDK r28 + OpenJDK 17)
make lib_install && make lib_android             # → libbox.aar (SDK23) + libbox-legacy.aar (SDK21)
```

The CI (`.github/workflows/lx-ci.yml`) builds the feature matrix (`baseline` / `xhttp` / `awg`
/ `full`), a cross-platform matrix and the Android `libbox.aar` (gomobile), running `check` on
the matching sample configs. Pushing a `v*-lx.*` tag runs `lx-release.yml`. It publishes as
GitHub Release assets the desktop binaries, `libbox-<ver>.aar` and `libbox-legacy-<ver>.aar`,
and a Windows 7 (32-bit) legacy binary: `sing-box-<ver>-windows-386-legacy-windows-7.zip`. The
Windows 7 binary is built with a Win7-patched Go and without `with_naive_outbound`: `cronet-go`
has no windows/386 build. Every other feature in it is unchanged.

## 13. The `lx` root block

The fork's global knobs live in one root block, `lx`, grouped by subsystem. Whatever describes a
single node stays in that node's object, as upstream does: AWG fields on the endpoint, `xhttp` in
`transport`, a masque node's own `idle_timeout`. Every sub-block and key is optional; an empty `lx`
is the same as none. A misspelt key is a load error, not a silent default.
The running config (`GetRunningConfig`) returns the `lx` block canonical: empty sub-blocks are dropped.

The sub-blocks and where they are described:

- [`lx.wg` — WireGuard / AmneziaWG endpoints](lx-block.md#lxwg--wireguard--amneziawg-endpoints)
- [`lx.masque` — MASQUE outbounds](lx-block.md#lxmasque--masque-outbounds)
- [`lx.trusttunnel` — TrustTunnel outbounds](lx-block.md#lxtrusttunnel--trusttunnel-outbounds)
- [`lx.mtu_align` — path MTU / packet-size alignment](lx-block.md#lxmtu_align--path-mtu--packet-size-alignment)
- [`lx.sniff_guard` — guard against rerouting by SNI](lx-block.md#lxsniff_guard--guard-against-rerouting-by-sni)
- [Removed keys](lx-block.md#removed-keys)
- [Reserved: `lx.naive`](lx-block.md#reserved-lxnaive)

The keys of each sub-block, load errors and examples are in [lx-block.md](lx-block.md).

---

## 14. Tailscale: turning Taildrop off

The `disable_taildrop` key on a `tailscale` endpoint. Without it Taildrop works as upstream.

```json
{ "type": "tailscale", "tag": "ts", "disable_taildrop": true }
```

With `true` the node accepts no files: the sender gets a 403 `Taildrop is disabled on this node`. Sending from the node is refused as well, and the status reports `canShareFiles = false`. Files received earlier stay in `taildrop_directory` and can still be opened and deleted. Other devices on the tailnet still see the node as a target: that is set by the Tailscale admin console, not by the node.

