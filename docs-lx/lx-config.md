# sing-box-lx — configuration of the downstream features

> 🌐 Русская версия: **[lx-config.ru.md](lx-config.ru.md)**.

> 🧭 **Where to look.** The fork's documentation has three levels, by the reader's question:
> [lx-config](lx-config.md) — what the fork has, every key in one line and how to enable it;
> features with their own logic have a separate document with examples: [lx-urltest](lx-urltest.md),
> [lx-dns-group](lx-dns-group.md), [lx-chain](lx-chain.md), [lx-block](lx-block.md),
> [lx-sniff](lx-sniff.md), [lx-energy](lx-energy.md), [lx-observability](lx-observability.md), [lx-impair](lx-impair.md);
> [protocols-transports](protocols-transports.md) — every field, type, default, error text;
> [xray-protocols-explained](xray-protocols-explained.md), [amneziawg-explained](amneziawg-explained.md) and [masque-explained](masque-explained.md) —
> how it works, why, how the fork does it and how it differs from vanilla.

## Table of contents

- [Features and build tags](#features-and-build-tags)
- [0. Every field at a glance (exhaustive example)](#0-every-field-at-a-glance-exhaustive-example)
- [1. XHTTP transport](#1-xhttp-transport)
- [2. AmneziaWG 2.0/3.x (AWG2, AWG3)](#2-amneziawg-203x-awg2-awg3)
- [3. urltest node selection modes](#3-urltest-node-selection-modes)
- [4. MASQUE outbound — Cloudflare WARP](#4-masque-outbound--cloudflare-warp)
- [5. DNS server group](#5-dns-server-group)
- [6. VLESS `encryption` — post-quantum layer](#6-vless-encryption--post-quantum-layer)
- [7. REALITY `key_share` — hybrid or classical ClientHello](#7-reality-key_share--hybrid-or-classical-clienthello)
- [8. Observability (CommandClient extensions)](#8-observability-commandclient-extensions)
- [9. ClientHello under `detour` — no default](#9-clienthello-under-detour--no-default)
- [10. `chain` outbound — a virtual multi-hop path of groups and nodes](#10-chain-outbound--a-virtual-multi-hop-path-of-groups-and-nodes)
- [11. Protocol sniffers for LAN traffic](#11-protocol-sniffers-for-lan-traffic)
- [12. Validate & build](#12-validate--build)
- [13. The `lx` root block](#13-the-lx-root-block)
- [14. Tailscale: turning Taildrop off](#14-tailscale-turning-taildrop-off)
- [15. `impair` outbound — a degraded link for testing](#15-impair-outbound--a-degraded-link-for-testing)

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
| **Root `lx` block** | — (always parsed) | `lx.wg`, `lx.masque`, `lx.trusttunnel`, `lx.detour`, `lx.sniff_guard` — the fork's global knobs, [lx-block.md](lx-block.md) | desktop + mobile |
| **DNS server group** | — (always built) | `dns.servers[].type: "group"` | desktop + mobile |
| **VLESS `encryption`** | — (always built) | `encryption` on a `vless` outbound | desktop + mobile |
| **`lxd` daemon** | `with_lxd` | not a config key — the `sing-box lxd` subcommand + `<state-dir>/daemon.json`; see [lxd-daemon.md](lxd-daemon.md) | desktop / server (not Win7, not AAR) |
| **`chain` outbound** | `with_lx_chain` | `outbounds[].type: "chain"` — a multi-hop path of groups and nodes | desktop + mobile |
| **`impair` outbound** | `with_lx_impair` | `outbounds[].type: "impair"` — a link degraded on purpose for testing, [§15](#15-impair-outbound--a-degraded-link-for-testing) | desktop + mobile |

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

XHTTP is the Xray transport (`splithttp`/`xhttp`): the proxy goes inside ordinary HTTP requests,
so it passes through a CDN. It attaches to VLESS, VMess and Trojan through the `transport` block
and works with TLS and REALITY; it is incompatible with XTLS-Vision. The HTTP version is set by
`tls.alpn`: HTTP/2 by default, `["h3"]` — HTTP/3, `["http/1.1"]` — HTTP/1.1. Second-version
fields are opt-in; without them the wire is the same as the first-version client. It needs the
`with_xhttp` build tag.

| Key | Default | What it sets |
|-----|---------|--------------|
| `mode` | `auto` | `auto` (`stream-one` on REALITY, `packet-up` otherwise), `packet-up`, `stream-up`, `stream-one` |
| `host` | SNI or server address | the `Host` header |
| `path` | `""` | the path prefix; the session id and packet number are appended as segments |
| `headers` | — | extra headers on every request |
| `x_padding_bytes` | `"100-1000"` | padding length in bytes, `"min-max"` or a number |
| `no_grpc_header` | `false` | do not send `Content-Type: application/grpc` in `stream-one` and `stream-up` |
| `session_placement` | `path` | where the session id goes: `path`, `query`, `header`, `cookie` |
| `session_key` | `X-Session` / `x_session` | the field name carrying the session id when it is not in the path |
| `seq_placement` | `path` | where the `packet-up` packet number goes: `path`, `query`, `header`, `cookie` |
| `seq_key` | `X-Seq` / `x_seq` | the field name carrying the packet number when it is not in the path |
| `session_table` | — | the alphabet of a random session id; without it the id is a UUID |
| `session_length` | — | the session id length, `"min-max"` or `"n"`; only with `session_table` |
| `uplink_data_placement` | `auto` (= `body`) | where upload data goes: `body`, `header`, `cookie`; the last two are `packet-up` only |
| `uplink_data_key` | `X-Data` / `x_data` | the header or cookie name carrying upload data |
| `uplink_chunk_size` | by placement | the upload chunk size in base64 characters, `"min-max"` |
| `uplink_http_method` | `POST` | the upload request method; `GET` is `packet-up` only |
| `x_padding_obfs_mode` | `false` | configurable `x_padding_*` padding instead of the padding in `Referer` |
| `x_padding_placement` | `queryInHeader` | where the padding goes: `cookie`, `header`, `query`, `queryInHeader` |
| `x_padding_key` | `x_padding` | the cookie or parameter name carrying the padding |
| `x_padding_header` | `X-Padding` | the header name carrying the padding |
| `x_padding_method` | `repeat-x` | `repeat-x` (`X` characters) or `tokenish` (base62) |
| `sc_max_each_post_bytes` | `"1000000-1000000"` | the threshold after which an upload POST is split |
| `sc_min_posts_interval_ms` | `"30-30"` | the pause between upload POSTs, ms |
| `xmux` | — | the HTTP connection pool: `max_connections` (`3-3`), `max_concurrency`, `c_max_reuse_times`, `h_max_request_times` (`600-900`), `h_max_reusable_secs` (`1800-3000`), `h_keep_alive_period` |

`sc_max_concurrent_posts`, `server_max_header_bytes`, `no_sse_header`, `sc_max_buffered_posts` and
`sc_stream_up_server_secs` are accepted for compatibility with Xray links; the client does not use
them.

```jsonc
{
  "type": "vless", "tag": "xhttp-out", "server": "example.com", "server_port": 443,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "tls": {
    "enabled": true, "server_name": "example.com",
    "utls": { "enabled": true, "fingerprint": "chrome" },
    "reality": { "enabled": true, "public_key": "<reality-public-key-base64>", "short_id": "0123abcd" }
  },
  "transport": { "type": "xhttp", "mode": "stream-one", "path": "/xhttp" }
}
```

📖 Every key, `xmux`, HTTP versions, examples behind a CDN and diagnostics are in
[protocols-transports.md §1](protocols-transports.md#1-xhttp-transport); how XHTTP works is in
[xray-protocols-explained.md](xray-protocols-explained.md).

---

## 2. AmneziaWG 2.0/3.x (AWG2, AWG3)

AmneziaWG is WireGuard that hides the shape of its packets from DPI: junk packets before the
handshake, padding, its own headers and decoys that look like another protocol. It is an ordinary
`wireguard` endpoint with extra fields at the endpoint root, as in the `[Interface]` section of an
`awg-quick` file; without them the endpoint works as plain WireGuard. The parameters are not
negotiated: the client and the server must match. AWG 3.x is the Amnezia `amnezia-awg2` container
with `protocol_version` 3.x. It needs the `with_awg` build tag.

| Key | Default | What it sets |
|-----|---------|--------------|
| `jc`, `jmin`, `jmax` | `0` | the number of junk packets before the handshake and their size |
| `s1`, `s2` | `0` | junk before the INIT and RESPONSE handshake messages |
| `s3`, `s4` | `0` | AWG 2.0: junk before the cookie reply and before every data packet; with `s4` the core sets `mtu` 1280 |
| `h1`–`h4` | `1`–`4`, as in WireGuard | magic headers of the four message types: a number or a non-overlapping `"min-max"` range |
| `i1`–`i5` | `""` | decoys before the handshake in tag format: `<b 0x…>`, `<r N>`, `<c>`, `<t>`, `<rc N>`, `<rd N>` |
| `id`, `ip`, `ib` | `""` | sugar instead of a hand-written `i1`: a `quic`, `dns`, `stun` or `sip` decoy with domain `id` and browser `ib` |
| `header_protection_key` | — | AWG 3.x: the header encryption key, set by the server; with it `s1`–`s4` are at least 12 |
| `content_padding_addition` | — | AWG 3.x: extra padding inside data packets, a number or `"min-max"` |
| `rekey_after_time`, `rekey_timeout`, `reject_after_time` | as in WireGuard | AWG 3.x: rekey timings, a number or `"min-max"` seconds |
| `keepalive_timeout`, `max_handshake_attempts` | as in WireGuard | AWG 3.x: the keepalive timeout and the number of handshake attempts, a number or `"min-max"` |
| `random_trailers` | `false` | AWG 3.x: a random-length tail on messages |
| `disable_cookies` | `false` | AWG 3.x: never send or require a cookie reply |
| `peers[].persistent_keepalive_interval` | — | the peer keepalive: seconds, or in AWG 3.x `"min-max"` |

```jsonc
{
  "type": "wireguard", "tag": "awg-out", "mtu": 1280,
  "address": ["10.0.0.2/32"], "private_key": "<client-private-key-base64>",
  "jc": 10, "jmin": 50, "jmax": 100,
  "s1": 20, "s2": 20, "s3": 60, "s4": 60,
  "h1": "43613244-384550127", "h2": "826869626-2105069164",
  "h3": "2124774725-2141151992", "h4": "2144594503-2146278491",
  "id": "www.google.com", "ip": "quic", "ib": "chrome",
  "peers": [{ "address": "server.example.com", "port": 51821,
    "public_key": "<server-public-key-base64>", "allowed_ips": ["0.0.0.0/0", "::/0"] }]
}
```

📖 Every key, the decoy format, the sugar profiles, the MTU budget, `awg.conf` mapping and an
AWG 3.1 example are in [protocols-transports.md §2](protocols-transports.md#2-amneziawg-203x-awg2-awg3);
how AmneziaWG works is in [amneziawg-explained.md](amneziawg-explained.md).

---

## 3. urltest node selection modes

Upstream `urltest` always selects the lowest-delay node. The fork adds two more modes:

| `mode` | Selection | Switches | Checked each `interval` |
|---|---|---|---|
| `least_test` (default) | the fastest node | when another one is faster by `tolerance` or a connection through the current one fails to open | every node |
| `round_robin` | a pool of live nodes | connections are spread over the pool | only the pool |
| `failover` | the fastest node at selection time | only when the current one fails or gets twice as slow as the others | only the current node |

`round_robin` keeps a site on its node (stickiness) and recovers from the connections
themselves: a connection that does not open through a dead node opens through the next one, and
the node is replaced with a live one at once. The `url`, `interval`, `idle_timeout` and
`interrupt_exist_connections` fields work as upstream; no build tag is needed.

| Key | Default | What it sets |
|-----|---------|--------------|
| `mode` | `least_test` | `least_test`, `round_robin` or `failover` |
| `balancer.pool` | `3` | how many nodes the `round_robin` pool holds |
| `balancer.pool_tolerance` | `0` | `0` — a pool of live nodes without comparing delays; `> 0` — the fastest ones, replaced when another wins by more than this many ms |
| `balancer.sticky_hash` | `["process","domain"]` | what the flow-to-node binding key is built from: `process`, `domain`, `source_ip`, `dest_ip`, `dest_port`; `["none"]` turns it off |

```jsonc
{
  "type": "urltest", "tag": "auto",
  "outbounds": ["proxy-a", "proxy-b", "proxy-c", "proxy-d", "proxy-e"],
  "interval": "15m",
  "mode": "round_robin",
  "balancer": { "pool": 3, "pool_tolerance": 0 }
}
```

📖 How each mode works, the pool self-heal, limits and examples are in
[lx-urltest.md](lx-urltest.md).

---

## 4. MASQUE outbound — Cloudflare WARP

A `masque` outbound connects to Cloudflare WARP and other CONNECT-IP ([RFC 9484](https://www.rfc-editor.org/rfc/rfc9484)) servers: whole IP
packets go over HTTP/3 or HTTP/2, and the core builds its own userspace stack on the tunnel, as for
a WireGuard endpoint. Keys are taken ready from the config: the client registers the device with
WARP. The default SNI is `www.cloudflare.com`, and the server is checked against the pinned
`public_key`. `vhttp: auto` tries HTTP/3 and moves to HTTP/2 after 3 s without an answer. It needs a
top-level `dns` block and the `with_quic` and `with_gvisor` tags (both in `LX_TAGS`).

| Key | Default | What it sets |
|-----|---------|--------------|
| `server`, `server_port` | required | the endpoint address and port, usually `443` |
| `private_key`, `public_key` | required for `cloudflare` | keys from the WARP registration, base64 of DER |
| `ip`, `ipv6` | at least one | the client's addresses inside the tunnel |
| `profile` | `cloudflare` | `cloudflare` — WARP, `standard` — your own [RFC 9484](https://www.rfc-editor.org/rfc/rfc9484) server |
| `vhttp` | `auto` | `auto`, `h3` or `h2` |
| `uri` | `https://cloudflareaccess.com` for `cloudflare` | the CONNECT-IP request address; required for `standard` |
| `tls` | `server_name`: `www.cloudflare.com` | the ordinary TLS block: `server_name`, `disable_sni`, `insecure`, `fragment`, `record_fragment` |
| `mtu` | `1280` | the inner packet size; up to `16000` on `h2` |
| `idle_timeout` | off, or `lx.masque.idle_timeout` | idle time after which the tunnel sleeps |
| `keep_alive_period` | `30s` | QUIC keepalive, `h3` only; a negative value turns it off |
| `network_list` | `tcp`, `udp` | which protocols go into the tunnel |
| `udp_fragment` | fragmentation allowed | `false` forbids fragmenting outer datagrams |

```jsonc
{
  "type": "masque", "tag": "warp",
  "server": "162.159.198.2", "server_port": 443,
  "private_key": "<base64 DER EC private key>",
  "public_key":  "<base64 DER PKIX public key>",
  "ip": "172.16.0.2/32", "ipv6": "2606:4700:110:...::/128"
}
```

📖 How it works and why, examples behind a relay and typical failures are in
[masque-explained.md](masque-explained.md); load errors and migration from the old shape are in
[protocols-transports.md §3](protocols-transports.md#3-masque-outbound-connect-ip--warp).

---

## 5. DNS server group

The `group` type in `dns.servers[]` puts several DNS servers behind one tag: if one server does
not answer, the query goes to the others. The group remembers servers' errors and wins and picks
among clean ones: `stable` holds the current one, `fastest` takes the one that most often answered
first, `parallel` asks everyone at once. The `hosts` key answers local names before the network is
asked. No build tag is needed.

| Key | Default | What it sets |
|-----|---------|--------------|
| `servers` | required | tags of the group's servers, at least one |
| `mode` | `stable` | `stable`, `fastest` or `parallel` |
| `error_ttl` | `2m` | how long a server's error is remembered |
| `win_ttl` | `5m` | how long a server's win is remembered; `fastest` only |
| `hosts` | — | tags of `hosts`-type servers asked first |

```jsonc
{ "type": "group", "tag": "public", "servers": ["google", "cloudflare"], "mode": "stable" }
```

📖 How the group picks a server, `hosts` for the servers' own addresses and examples are in
[lx-dns-group.md](lx-dns-group.md).

## 6. VLESS `encryption` — post-quantum layer

The `encryption` field on a `vless` outbound turns on the `mlkem768x25519plus` handshake inside
VLESS. The layer sits below VLESS and above the transport, independent of REALITY. A server with
`decryption` configured silently drops a connection without this layer right after the transport
comes up; the field fixes that. The fork carries only the client half; no build tag is needed.

| Key | Default | What it sets |
|-----|---------|--------------|
| `encryption` | `""` | `""` or `"none"` — the layer is off; otherwise the string `mlkem768x25519plus.<native\|xorpub\|random>.<0rtt\|1rtt>[.<padding>…].<key>` |

```jsonc
{
  "type": "vless", "tag": "pq-node", "server": "example.com", "server_port": 443,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "encryption": "mlkem768x25519plus.native.0rtt.<base64url ML-KEM-768 key>",
  "transport": { "type": "ws", "path": "/ws" }
}
```

> ⚠️ In a subscription the value comes as `settings.vnext[0].users[0].encryption`. A config
> generator must move it to the flat `encryption` field next to `uuid`.

📖 The string segments, load errors and the subscription check are in
[protocols-transports.md §5.5](protocols-transports.md#55-the-encryption-field-on-vless); how the
layer works is in [xray-protocols-explained.md §5](xray-protocols-explained.md#5-vless-encryption-the-post-quantum-layer).

---

## 7. REALITY `key_share` — hybrid or classical ClientHello

The `chrome`, `firefox` and `safari` browser fingerprints send the hybrid post-quantum
`X25519MLKEM768` key share, and the ClientHello becomes three times longer: two TCP segments
instead of one. New Xray servers (from v26.9.8) require the hybrid, while some networks lose such
a hello. The core does not choose between "the server rejects" and "the network loses", so the
choice is set per node.

| Key | Default | What it sets |
|-----|---------|--------------|
| `tls.reality.key_share` | `""` | `""` — as the fingerprint carries; `"classical"` — strip the hybrid (Xray < v26.9.8 only); `"hybrid"` — require the hybrid, an error at once on a fingerprint without it |

```json
"reality": { "enabled": true, "public_key": "…", "short_id": "0123abcd", "key_share": "classical" }
```

For a new server on such a network, ClientHello splitting (`record_fragment`) and `detour` remain.

📖 Which fingerprint sends what, hello sizes and errors are in
[protocols-transports.md §5.2](protocols-transports.md#52-fingerprints-and-the-hybrid-key-share);
how REALITY works is in [xray-protocols-explained.md §2](xray-protocols-explained.md#2-reality).

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
| `GetEffectiveConfig()` | the same config after the lx pre-pass: chain hops added, `detour` of taken-over nodes rewritten, MTU aligned |
| `GetOutboundConfig(tag)` | one node as written, as built, and the list of changes between them |
| `SubscribeDNSQueries(includeAnswers, handler)` | a live stream of DNS queries |
| `GetChainStatus(tag)` | the state of one `chain` outbound by position |
| `SetEndpointEnabled(tag, enabled)` | turns a WG/AWG endpoint off and on at runtime |

Connections in the stream carry a `detourList` field — the path through `detour` to the final
outbound.

```sh
sing-box api group pool auto     # the round_robin pool of group auto (a build with with_lx_command)
```

The fields of each method and their values are in [lx-observability.md](lx-observability.md).

---

## 9. ClientHello under `detour` — no default

A relay that reads the SNI from the ClientHello and sends the connection to that name (Xray
`sniffing` with `destOverride` without `routeOnly`) takes a node with a masking SNI (REALITY, WARP)
to someone else's host. Such a relay cannot parse a split ClientHello. Without the
`lx.sniff_guard` block the core splits the ClientHello as upstream — only on nodes with `fragment`
or `record_fragment`; under a `detour` without a kernel socket below, the node's `fragment` is
dropped by `lx.detour.strip` by default, `record_fragment` stays. The block turns protection on for paths through relays and learns from
connection outcomes; its keys are in [§13](#13-the-lx-root-block).

```json
"lx": { "sniff_guard": { "tls": { "method": "record_fragment" } } }
```

📖 The measurement, the old default, node keys and every mode of the block are in
[lx-block.md](lx-block.md#lxsniff_guard--guard-against-rerouting-by-sni).

---

## 10. `chain` outbound — a virtual multi-hop path of groups and nodes

The `chain` outbound builds a multi-hop path from existing nodes and groups. Each position is chosen
by its own group with all of its logic, and the chain follows the choice. At config load every node
reached on a position ≥ 1, group members included, gets `detour` onto the hop of the position below,
`<tag>#<i−1>`, an internal outbound of type `chain-hop`; the node's own `detour` is overridden. A
node keeps one instance and one path, and a group on a position checks the instances that carry
its traffic. MTU alignment and the strip filters of `lx.detour` act on these nodes as on any node
under `detour`. It needs the `with_lx_chain` build tag (in the desktop `LX_TAGS` and in the AAR).

| Key | Default | What it sets |
|-----|---------|--------------|
| `outbounds` | required | position tags in packet order, from the client to the exit; at least two, all different |
| `rewrite` | `{}` | a merge-patch to the config of taken-over nodes by node type at positions ≥ 1; it cannot touch `detour` |
| `interrupt_exist_connections` | `false` | on a position toggle, also cut the user connections through the chain |

`idle_timeout`, `strip_evasion` and `strip` in a chain fail the load and name the replacement.

```jsonc
{
  "type": "chain",
  "tag": "via-three",
  "outbounds": ["selector-in", "selector-mid", "selector-exit"],
  "rewrite": { "wireguard": { "mtu": 1200 } }
}
```

How the hops work, the load errors, the toggle, observability and limits are in [lx-chain.md](lx-chain.md).

---

## 11. Protocol sniffers for LAN traffic

The fork adds five protocol names for the `sniffer` list of the `sniff` action and for the
`protocol` matcher in rules. A router recognises VPN tunnels and calls of LAN devices by their
first packet and routes them by rules. The fork's sniffers run before the upstream uTP sniffer,
which would otherwise label WireGuard as `bittorrent`. Only the first packet is looked at, so
obfuscated tunnels and RTP are not recognised.

| Name | Network | What it recognises |
|------|---------|--------------------|
| `wireguard` | UDP | WireGuard handshake and data packets by size |
| `openvpn` | UDP, TCP | the first OpenVPN packet: plain, tls-auth, tls-crypt |
| `ike` | UDP | IKE (IPsec): `IKE_SA_INIT` |
| `tailscale` | UDP | the Tailscale disco ping; the tunnel itself shows as `wireguard` |
| `sip` | UDP, TCP | a SIP request; sets `domain` from the Request-URI |

```jsonc
{ "protocol": ["wireguard", "openvpn", "ike", "tailscale"], "outbound": "warp" }
```

📖 The signs of each protocol, sniffer order, a router example and what is not recognised are in
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

The fork's global knobs live in one root `lx` block, by subsystem. Everything that describes a
specific node stays in its object, as upstream. All sub-blocks and keys are optional; an empty
`lx` equals an absent one, and a misspelt key is a load error. `GetRunningConfig` returns the block
canonical: empty sub-blocks are dropped.

| Key | Default | What it sets |
|-----|---------|--------------|
| `wg.idle.suspend` | off | idle time after which a WG/AWG endpoint outside the active route sleeps; mobile build only |
| `wg.idle.suspend_reachable` | off | the same for endpoints in the active route; not less than `suspend` |
| `wg.idle.teardown` | = `suspend_reachable` | how long an endpoint sleeps before a full teardown; `"0"` — never |
| `wg.idle.lazy_build` | `false` | build the device on first connection, not at start |
| `wg.idle.build_max` | `0` | at most N built devices at once; `0` — no cap |
| `wg.idle.build_overflow` | `wait` | at the cap: `wait` — wait, `build` — build past the cap |
| `wg.buffers.read_datagram_count` | `128` | datagrams per socket read, 1–128; fewer — less memory |
| `wg.buffers.send_by_mtu` | `true` | read buffers by the tunnel MTU; `false` — 64 KB, for diagnostics |
| `wg.connected_socket` | `false` | connect the tunnel socket to the server address; Linux and Android |
| `masque.idle_timeout` | off | one sleep window for `masque` nodes without their own `idle_timeout` |
| `trusttunnel.idle_timeout` | `5m` | one idle window for `trusttunnel` nodes without their own `idle_timeout` |
| `detour` | `true` | what the core does to a node under `detour`: `true` — MTU alignment `clamp` and every strip filter, `false` — upstream behaviour, or an object |
| `detour.mtu_align` | `clamp` | fitting MTU and packet size of nodes above tunnels: `off`, `fill`, `clamp` |
| `detour.strip` | every filter | filters for a node without a kernel socket below: `true`, `false`, or `{filters: {tls: {fragment, spoof, kernel, engine}}}`; an object of filters replaces the catalog |
| `detour.except_outbounds` | none | tags neither aligned nor filtered |
| `sniff_guard.nodes.sni_mismatch` | `true` | protect only nodes whose SNI differs from `server` |
| `sniff_guard.nodes.outbound_type` | every type | protect only nodes of these types |
| `sniff_guard.detour_type` | `direct`, `ssh`, `socks` — `clean`, others — `suspect` | the starting path label by relay type: `never`, `clean`, `suspect` |
| `sniff_guard.override` | `false` | the block wins over the node's `fragment` and `record_fragment` |
| `sniff_guard.tls.method` | `off` | TLS protection: `record_fragment`, `fragment`, `delay`, `off` |
| `sniff_guard.quic.method` | `off` | QUIC protection: `decoy_first_byte`, `delay`, `off` |
| `sniff_guard.tls/quic.delay` | `300ms` | the pause of the `delay` method |
| `sniff_guard.tls/quic.persist` | `false` | keep relay labels in `experimental.cache_file` |
| `sniff_guard.tls/quic.detour_type` | the shared map | a separate `detour_type` map for TLS or QUIC |

```jsonc
"lx": {
  "wg": { "idle": { "suspend": "30s", "suspend_reachable": "5m" } },
  "masque": { "idle_timeout": "5m" },
  "detour": { "mtu_align": "clamp", "strip": true },
  "sniff_guard": {
    "tls":  { "method": "record_fragment" },
    "quic": { "method": "decoy_first_byte" }
  }
}
```

Removed keys (`lx.quic_sniff_decoy`, `lx.awg_masquerade`, the flat `lx.wg` idle keys,
`route.lx_idle_*`) are a load error; what to set instead is in
[lx-block.md](lx-block.md#removed-keys).

📖 Each sub-block with its logic, load errors and examples is in [lx-block.md](lx-block.md); the
sleep energy model is in [lx-energy.md](lx-energy.md).

---

## 14. Tailscale: turning Taildrop off

| Key | Default | What it sets |
|-----|---------|--------------|
| `disable_taildrop` | `false` | on a `tailscale` endpoint: do not accept or send Taildrop files |

```json
{ "type": "tailscale", "tag": "ts", "disable_taildrop": true }
```

With `true` the node accepts no files: the sender gets a 403 `Taildrop is disabled on this node`. Sending from the node is refused as well, and the status reports `canShareFiles = false`. Files received earlier stay in `taildrop_directory` and can still be opened and deleted. Other devices on the tailnet still see the node as a target: that is set by the Tailscale admin console, not by the node.

---

## 15. `impair` outbound — a degraded link for testing

The `impair` outbound degrades the connections that go through it on purpose: a bottleneck with a
queue, delay and its jitter, bursty loss, rate swings, stalls and outages on a schedule, for all
connections or only some. It dials like
`direct` or through its own `detour`, degrades both directions from one end, and needs no root and
no netem. Use it to test balancing, failover and chains against real servers over a bad link. The
build tag `with_lx_impair` is in the desktop `LX_TAGS` and in the AAR; until a config declares
`"type": "impair"` it does nothing. On start `impair` logs a warning that the link is degraded on
purpose.

Outbound keys besides the dial keys of `direct`:

| Key | Default | What it sets |
|-----|---------|--------------|
| `link_params` | no degradation | the outbound's line: every link key lives here |
| `sublinks` | none | a second stage for matching connections: one site, a port, a share of flows |
| `schedule`, `schedule_repeat` | none | changes of the line and the sublinks over time; the length of a repeating round |
| `start` | `process` | what the schedule counts from: process start or the first traffic (`first_use`) |
| `seed` | random | the seed: the same one gives the same losses and swings |

`link_params` — the same object in the outbound, a sublink, a schedule entry, `impair set` and a
profile:

| Key | Default | What it sets |
|-----|---------|--------------|
| `profile` | none | a template: `dsl`, `lte-walk`, `wifi-lossy` or a name from `lx.impair_profiles`; the other keys tune it |
| `rate`, `queue` | unlimited, `"50ms"` | bottleneck rate (`"10mbit"`) and the queue before it (time or size) |
| `delay`, `jitter` | `0` | added one-way delay and its spread |
| `loss`, `loss_burst` | `0`, `1` | share of loss (`"0.5%"`) and mean length of a loss run |
| `vary`, `stall` | none | rate swings and random stops of the line; `false` turns them off |
| `outage`, `reset` | `false` | the line silently goes away; `reset` breaks the connections (schedule, `impair set`, sublink only) |
| `up`, `down` | — | the same keys for one direction |

The main placement is under a proxy node: the proxy points at `impair` with its `detour`, and the
road to the proxy server is degraded.

```jsonc
{"type": "vless", "tag": "via-se", "server": "se.example.net", "server_port": 443, "detour": "slow-se" /* … */},
{"type": "impair", "tag": "slow-se", "seed": 1, "link_params": {"rate": "10mbit", "delay": "20ms", "loss": "0.5%"}}
```

The link changes at runtime with `sing-box api impair set|schedule|status` and the matching gRPC
commands.

Every key with its range, sublinks, the schedule, profiles, runtime control, how datagrams and
streams are degraded, the errors and recipes for `round_robin` and `failover` tests — in
[lx-impair.md](lx-impair.md).
