# sing-box-lx — configuration of the downstream features

> 🌐 Русская версия: **[lx-config.ru.md](lx-config.ru.md)**.

`sing-box-lx` is upstream [sing-box](https://github.com/SagerNet/sing-box) plus a small set of **client-side** features, each gated behind a build tag:

| Feature | Build tag | Where it lives in config | Included in |
|---------|-----------|--------------------------|-------------|
| **XHTTP** transport (Xray-compatible) | `with_xhttp` | `transport.type: "xhttp"` on a VLESS / VMess / Trojan outbound | desktop + mobile |
| **AmneziaWG 2.0** (AWG2) | `with_awg` | extra fields on a `wireguard` **endpoint** | desktop + mobile |
| **MASQUE** outbound (CONNECT-IP / WARP) | `with_quic`+`with_gvisor` | `outbounds[].type: "masque"` | desktop + mobile |
| **Idle-suspend** (SPEC 020) | `with_lx_idle_suspend` | `route.lx_idle_suspend` (+ `lx_idle_suspend_reachable`, `lx_idle_teardown`) | **mobile only** (AAR) |
| **DNS server group** (SPEC 033/035) | — (always built) | `dns.servers[].type: "group"` | desktop + mobile |
| **VLESS `encryption`** (SPEC 032) | — (always built) | `encryption` on a `vless` outbound | desktop + mobile |
| **`lxd` daemon** (SPEC 055–057, 063–068) | `with_lxd` | not a config key — the `sing-box lxd` subcommand + `<state-dir>/daemon.json`; see [lxd-daemon.md](lxd-daemon.md) | desktop / server (**not** Win7, **not** AAR) |
| **`chain` outbound** (SPEC 073) | `with_lx_chain` | `outbounds[].type: "chain"` — a multi-hop path of groups and nodes | desktop + mobile |

Build the desktop/CLI binary: `make -f Makefile.lx lx-build` (output `sing-box`, version `…-lx.N`) — this bundles `with_xhttp` + `with_awg` (+ `with_lx_command`, `with_lxd`), but **not** `with_lx_idle_suspend`.
Without a tag the feature is absent: an `xhttp` transport or an AWG field is rejected at load time with an explicit error (no silent downgrade).

**`with_lx_idle_suspend` is mobile-only** and is added only to the Android/iOS AAR
(`cmd/internal/build_libbox`), not to the desktop `LX_TAGS`. It suspends idle +
unreachable WireGuard/AmneziaWG endpoints to free their recv-worker buffers (the
GC-heat / RAM holder, ~8 MB each where `BatchSize=128` — Android/Linux; measured
on-device: 8 endpoints suspended → 134 MB freed). A desktop build has small
`BatchSize`, so the feature would save almost nothing there; to avoid a silent
mismatch, a desktop/CLI binary that is handed a config with `route.lx_idle_suspend`
**fails fast at start**: `route.lx_idle_suspend is set but this build lacks
idle-suspend support; rebuild with -tags with_lx_idle_suspend (mobile-only feature)`.
See the [ENERGY feature](../SPECS/FEATURES/008-ENERGY/FEATURE.md).

Related keys (2026-07-15 revision): `route.lx_idle_suspend_reachable` — optional
second, longer idle window after which even *reachable* endpoints (pool members,
the selected node, final) suspend; `route.lx_idle_teardown` — the third level:
how long an endpoint may *sleep* before a full teardown (Close, the gVisor
netstack goes too; wake = rebuild ~0.5–1 s; defaults to the reachable window);
`urltest.passive_check` — skip health probes
while a recent successful TCP dial proves the node alive. The full energy model,
timelines and the recommended mobile configuration live in
**[lx-energy.md](lx-energy.md)**.

> ⚠️ All keys/UUIDs below are **placeholders**. Never commit real private keys / pre-shared keys to a repository.

---

## 0. Every field at a glance (exhaustive example)

One config carrying every field of the **outbound-side** features — XHTTP transport,
AmneziaWG 2.0 endpoint, the `id`/`ip`/`ib` masquerade sugar, VLESS `encryption`, and the
`urltest` `round_robin` balancer (MASQUE, the DNS group and the `route.lx_idle_*` keys have
their own examples in [§4](#4-masque-outbound--cloudflare-warp-spec-021), [§5](#5-dns-server-group-spec-033035)
and [lx-energy.md](lx-energy.md)). This is a **kitchen-sink reference**, not a recommended config: many fields are
mutually exclusive (e.g. the `id`/`ip`/`ib` sugar vs. a hand-written `i1`) or are server-only
and ignored by the client — those are flagged inline. For a working setup, copy only the block
you need and read its section below. Each comment shows the **default** and the **allowed values**.

```jsonc
{
  "outbounds": [
    // ─────────────────────────────────────────────────────────────────────────
    // XHTTP transport (§1) — attaches to a VLESS / VMess / Trojan outbound.
    // Needs the with_xhttp build tag.
    // ─────────────────────────────────────────────────────────────────────────
    {
      "type": "vless",
      "tag": "xhttp-out",
      "server": "example.com",
      "server_port": 443,
      "uuid": "00000000-0000-0000-0000-000000000000",
      "encryption": "",                         // default: "" (off). VLESS post-quantum layer (§6):
                                                //   "mlkem768x25519plus.<native|xorpub|random>.<0rtt|1rtt>….<key>"
      "tls": {
        "enabled": true,
        "server_name": "example.com",
        "utls": { "enabled": true, "fingerprint": "chrome" },
        "reality": { "enabled": true, "public_key": "<reality-public-key-base64>", "short_id": "0123abcd" }
      },
      "transport": {
        "type": "xhttp",                        // selector — must be "xhttp"

        // ── core (v1) ──
        "mode": "auto",                         // auto | packet-up | stream-up | stream-one.
                                                //   auto → stream-one on Reality, else packet-up
        "host": "example.com",                  // default: TLS SNI / server. Overrides Host header
        "path": "/xhttp",                       // default: "" (root). Session id / seq appended as path segments
        "headers": { "X-Foo": "bar" },          // default: none. Extra headers on every request
        "x_padding_bytes": "100-1000",          // default: "100-1000". "min-max" or single int — padding length
        "no_grpc_header": false,                // default: false. Omit Content-Type: application/grpc on stream-one/stream-up

        // ── session / seq placement (v2) ──
        "session_placement": "path",            // default: path. path | query | header | cookie
        "session_key": "",                      // default: X-Session (header) / x_session (query|cookie); unused for path
        "seq_placement": "path",                // default: path. path | query | header | cookie (packet-up)
        "seq_key": "",                          // default: X-Seq (header) / x_seq (query|cookie); unused for path
        "session_table": "",                    // default: unset (dashed UUID id). Alphabet name or literal ASCII set
        "session_length": "",                   // default: unset. "min-max" or "n"; set together with session_table

        // ── uplink-data placement (v2, packet-up) ──
        "uplink_data_placement": "auto",        // default: auto (== body). body | auto | header | cookie.
                                                //   header/cookie ONLY valid in packet-up
        "uplink_data_key": "",                  // default: X-Data (header) / x_data (cookie); "" for body
        "uplink_chunk_size": "",                // default: cookie 2048-3072 / header 3000-4000 / else = sc_max_each_post_bytes.
                                                //   "min-max" base64 chars, min floored to 64
        "uplink_http_method": "POST",           // default: POST. Upper-cased. GET only in packet-up

        // ── X-Padding obfuscation (v2) — all below apply only when obfs_mode=true ──
        "x_padding_obfs_mode": false,           // default: false. Master switch. false → legacy Referer x_padding
        "x_padding_placement": "queryInHeader", // default: queryInHeader. cookie | header | query | queryInHeader
        "x_padding_key": "x_padding",           // default: x_padding. Cookie/query param name
        "x_padding_header": "X-Padding",         // default: X-Padding. Header name (header / queryInHeader placement)
        "x_padding_method": "repeat-x",         // default: repeat-x. repeat-x ('X'*N) | tokenish (HPACK-tuned base62)

        // ── packet-up tuning (v2) ──
        "sc_max_each_post_bytes": "1000000-1000000", // default: "1000000-1000000". POST split threshold ("min-max")
        "sc_min_posts_interval_ms": "30-30",    // default: "30-30". Anti-burst delay between POSTs, ms ("min-max")

        // ── accepted but IGNORED by the client (config/link symmetry only) ──
        "sc_max_concurrent_posts": 0,           // IGNORED — legacy Xray knob; client serializes one POST in flight
        "server_max_header_bytes": 0,           // IGNORED — server-only (http.Server.MaxHeaderBytes)
        "no_sse_header": false,                 // IGNORED — server-only (SSE Content-Type on downlink)
        "sc_max_buffered_posts": 0,             // IGNORED — server-only (upload reorder buffer depth)
        "sc_stream_up_server_secs": ""          // IGNORED — server-only (stream-up keepalive interval)
      }
    },

    // ─────────────────────────────────────────────────────────────────────────
    // AmneziaWG 2.0 endpoint (§2) — a wireguard endpoint with obfuscation.
    // Needs the with_awg build tag. All AWG fields sit at the endpoint ROOT
    // (none on a peer), mirroring an awg-quick .conf [Interface] section.
    // ─────────────────────────────────────────────────────────────────────────
    {
      "type": "wireguard",
      "tag": "awg-out",
      "system": false,
      "mtu": 1280,                              // lower than plain WG (s4 transport junk); core defaults 1280 if s4 set
      "address": ["10.0.0.2/32"],
      "private_key": "<client-private-key-base64>",

      // ── junk packets before the handshake ──
      "jc": 10,                                 // default: 0 (unset). Count of junk packets
      "jmin": 50,                               // default: 0. Min size of each junk packet
      "jmax": 100,                              // default: 0. Max size of each junk packet (keep below path MTU)

      // ── handshake-message junk (s1/s2) + AWG 2.0 junk-size (s3/s4) ──
      "s1": 20,                                 // default: 0. Junk prepended to the handshake INIT message
      "s2": 20,                                 // default: 0. Junk prepended to the handshake RESPONSE message
      "s3": 60,                                 // default: 0. AWG 2.0 junk-size param
      "s4": 60,                                 // default: 0. AWG 2.0 junk-size param

      // ── magic headers: single uint32 (AWG 1.x) OR "min-max" range (AWG 2.0) ──
      // ranges must NOT overlap; 0 / "" = unset (counts as the WG default 1/2/3/4)
      "h1": 1234567890,                         // or "43613244-384550127"   — WG message type 1
      "h2": 1234567891,                         // or "826869626-2105069164" — type 2
      "h3": 1234567892,                         // or "2124774725-2141151992" — type 3
      "h4": 1234567893,                         // or "2144594503-2146278491" — type 4

      // ── CPS decoy packets i1..i5 (case-sensitive; sent in order pre-handshake) ──
      // tags: <b 0xHEX> bytes, <c> counter, <t> timestamp, <r N> random bytes,
      //       <rc N> random chars, <rd N> random digits.
      // i1 is MUTUALLY EXCLUSIVE with the id/ip/ib sugar below — use one or the other.
      "i1": "<b 0x000100002112a442><r 12>",     // default: "". CPS packet 1
      "i2": "<b 0x010100002112a442><r 12>",     // default: "". CPS packet 2
      "i3": "<r 24>",                            // default: "". CPS packet 3
      "i4": "",                                  // default: "". CPS packet 4
      "i5": "",                                  // default: "". CPS packet 5

      // ── masquerade sugar (§2 → id/ip/ib) — generates i1 for you.
      //    MUTUALLY EXCLUSIVE with an explicit i1 above. Shown here for reference;
      //    in a real config use EITHER i1..i5 OR id/ip/ib, not both. ──
      "id": "www.google.com",                   // default: "". Masquerade domain (SNI/QNAME/SIP host). Required for ip=quic
      "ip": "quic",                             // default: "". quic | dns | stun | sip
      "ib": "chrome",                           // default: "". chrome | firefox | curl. Only meaningful with ip=quic

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
    },

    // ─────────────────────────────────────────────────────────────────────────
    // urltest round_robin load balancing (§3). Config fields always available;
    // the GetPool client method needs with_lx_command.
    // ─────────────────────────────────────────────────────────────────────────
    {
      "type": "urltest",
      "tag": "auto",
      "outbounds": ["xhttp-out", "proxy-b", "proxy-c", "proxy-d", "proxy-e"],
      "url": "https://www.gstatic.com/generate_204",
      "interval": "15m",
      "passive_check": false,                   // default: false. Recent successful TCP dial counts
                                                //   as proof of life while fresh (< interval) — probes stay quiet
      "mode": "round_robin",                    // default: least_test. least_test | round_robin
      "balancer": {                             // only valid with mode: round_robin
        "pool": 3,                              // default: 3. 0/omitted → 3; effective = min(pool, #outbounds)
        "pool_tolerance": 0,                    // default: 0 (ms). 0 = keep-live-fill; >0 = top-N-by-delay hysteresis
        "sticky_hash": ["process", "domain"]    // default: ["process","domain"]. Components:
                                                //   process | domain | source_ip | dest_ip | dest_port.
                                                //   DISABLE with the sentinel ["none"] — never a bare []
      }
    }
  ]
}
```

> **Field count:** 26 XHTTP + 21 AmneziaWG (incl. `id`/`ip`/`ib`) + 1 VLESS (`encryption`) +
> 6 `urltest` (`mode`, `passive_check` + `balancer{pool,pool_tolerance,sticky_hash}`). Mutually-exclusive / ignored fields are
> labelled inline above; the sections below give the per-field semantics, gotchas and live
> verification status.

---

## 1. XHTTP transport

XHTTP (Xray "splithttp"/"xhttp") is a v2ray transport that tunnels the proxy over plain HTTP/2 requests. It attaches to VLESS / VMess / Trojan via the shared `transport` block and composes with TLS, including **Reality**. (XHTTP is incompatible with XTLS-Vision — that is a protocol limitation, not ours.) The default wire shape is **byte-identical to the live-verified v1 client** — every v2 field (session/seq placement, uplink obfs, the `x_padding_*` family, `xmux` connection reuse) is opt-in.

A minimal `transport` block is just `"type": "xhttp"` (mode `auto`); the [example below](#example--vless--xhttp--reality) adds a Reality node with `stream-one`.

> **📖 The full field reference — all 26 XHTTP keys, their defaults, the `xmux` pool
> semantics, range value forms and a troubleshooting table — is in
> [lx-protocols-transports.md §1](lx-protocols-transports.md#1-xhttp-transport)**
> ([RU](lx-protocols-transports.ru.md#1-xhttp-транспорт)).

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

## 2. AmneziaWG 2.0 (AWG2)

AWG is WireGuard + DPI-evasion obfuscation. It is configured as a normal sing-box **`wireguard` endpoint** with extra promoted fields. With `with_awg` these are pushed to the device; a config without any AWG field is a plain WireGuard endpoint (byte-identical to upstream behavior).

AWG2 = AWG1 fields **plus** the CPS packets `I1`–`I5`. Both client and server must run AmneziaWG with **matching** parameters (the I-packets are configuration, not negotiated). For a friendlier way to set the first decoy, the WireSock-style `id`/`ip`/`ib` sugar generates `i1` for you — see the [full reference](lx-protocols-transports.md#25-masquerade-sugar-id--ip--ib).

The AWG fields sit at the endpoint **root** (none on a peer), mirroring an `awg-quick`
`.conf` `[Interface]` section: junk (`jc`/`jmin`/`jmax`), handshake padding (`s1`–`s4`),
magic headers (`h1`–`h4`, single value or `"min-max"` range), and CPS decoys (`i1`–`i5`).
An AWG endpoint needs a **lower `mtu`** than plain WireGuard because `s4` pads every data
packet — the core defaults to `1280` when you set `s4` and omit `mtu`.

> **📖 The full field reference — every junk/signature/magic/CPS field with its type
> and default, the CPS tag format, the `id`/`ip`/`ib` masquerade sugar (four profiles,
> which to pick, what reaches the wire), the MTU budget math, the `awg.conf` 1:1 mapping
> and the verbatim validation errors — is in
> [lx-protocols-transports.md §2](lx-protocols-transports.md#2-amneziawg-20-awg2)**
> ([RU](lx-protocols-transports.ru.md#2-amneziawg-20-awg2)).

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

The **`id`/`ip`/`ib` masquerade sugar** (four decoy profiles: `quic`/`dns`/`stun`/`sip`),
the **MTU budget** (why `s4` forces a lower MTU, the `sendmsg: message too long` symptom,
the auto-`1280` default, `udp_fragment` for nested tunnels), and the **`awg.conf` 1:1
mapping** are all documented in the full reference — see the 📖 link above.

The runtime is backed by `Leadaxe/wireguard-go` (sagernet/wireguard-go + AmneziaWG obfuscation, wired via the `submodules/wireguard-go` submodule) — see the [AWG2 feature](../SPECS/FEATURES/003-AWG2/FEATURE.md).

---

## 3. round_robin load balancing (SPEC 019)

Upstream `urltest` always selects the single lowest-delay node. sing-box-lx adds a
`round_robin` **mode** that rotates traffic over a fixed-size **pool** of nodes — built to
scale to large node lists (only the pool is health-checked, not every node). Selection
happens once per connection; a UDP/QUIC session stays on its node. With `mode` omitted (or
`least_test`) the outbound behaves exactly like upstream and `balancer` must not be set.

The `GetPool` CommandClient method (see [§7](#7-observability-commandclient-extensions)) is
behind `with_lx_command`; the `mode`/`balancer` config fields themselves are always available.

### Fields (on a `urltest` outbound)

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `mode` | string | `least_test` | `least_test` (upstream behaviour) \| `round_robin` (rotate over the pool). `least_connection` is rejected (round_robin is statistically even) |
| `passive_check` | bool | `false` | a recent successful TCP dial counts as proof of life while fresh (< `interval`): `least_test` skips whole re-test cycles while the selected node is passively confirmed; `round_robin` (only with `pool_tolerance: 0`) treats confirmed slots as live without probing. Cost: staler delay numbers in the UI. See [lx-energy.md](lx-energy.md) |
| `balancer` | object | — | round_robin parameters; **only valid with `mode: round_robin`** (error otherwise). The upstream `tolerance` field is ignored in round_robin — use `pool_tolerance` instead (a startup warning points this out while `pool_tolerance` is unset) |

#### `balancer` fields

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `pool` | int | `3` | rotation pool size. `0`/omitted → `3`; negative is an error. Effective size is `min(pool, number of outbounds)`. Only the pool is URL-tested each `interval`, so a list of hundreds of nodes does **not** mean hundreds of tests |
| `pool_tolerance` | int (ms) | `0` | `0` = keep the pool full of **live** nodes (delay not ranked), testing no more than needed — the cheap mode for large lists. `> 0` = test **all** nodes and keep the fastest `pool`; a member is replaced only when an outside node beats it by more than `pool_tolerance` ms (hysteresis). A dead pool node keeps its slot until a live replacement is found (the pool never empties); a dial error never changes the pool — only the periodic health-check does |
| `sticky_hash` | string[] | `["process","domain"]` | flow-stickiness key components (see below). Omitted/`[]` → the default. **To disable** stickiness use the sentinel **`["none"]`** — never a bare `[]`. Components: `process` \| `domain` \| `source_ip` \| `dest_ip` \| `dest_port` |

> **badjson `[]` caveat.** Do not write `"sticky_hash": []` to turn stickiness off: the
> sing-box config decoder re-marshals each outbound and an empty JSON array does not survive
> the round-trip — it collapses to "omitted", which means *default* (`["process","domain"]`,
> i.e. stickiness **on**). Use the explicit **`["none"]`** sentinel; it is the only element
> allowed when present (mixing `none` with a real component is an error).

### Slot-hash binding

`sticky_hash` binds a flow to a fixed **slot index** — `slot[hash(key) % pool]` (FNV-64a over
the concatenated components) — not to a node position. Slots never move and a replacement node
takes the exact slot it evicts, so a node that stays in its slot keeps all of its keys when
other slots change: no needless reconnects and no per-key state. The default `["process","domain"]`
gives per-process, per-destination-domain affinity; `domain` reads the original sniffed domain
(it survives the router's domain→IP resolve, so it is populated for normal domain traffic, not
only literal-IP destinations). For domain-based traffic keep `domain` in the key — a key of only
`source_ip`/`dest_ip`/`dest_port` can collapse to `""` for an unresolved destination, sticking
every flow of one source to a single slot.

### Example — urltest with round_robin

```jsonc
{
  "type": "urltest",
  "tag": "auto",
  "outbounds": ["proxy-a", "proxy-b", "proxy-c", "proxy-d", "proxy-e"],
  "interval": "15m",
  "mode": "round_robin",
  "balancer": {
    "pool": 3,
    "pool_tolerance": 0,
    "sticky_hash": ["process", "domain"]   // ["none"] to disable stickiness
  }
}
```

> **Status.** Even rotation is verified locally (10/10/10 with stickiness off) and
> **device-verified end to end** on a real multi-node pool — the rc.15 `domain` fix takes
> on-device per-domain uniformity from ~0.27 to 0.95+. For a large node list, `pool_tolerance: 0`
> + a small `pool` + a longer `interval` is the recommended, lowest-overhead setup.

**📖 [Full reference →](../docs/configuration/outbound/urltest.md)** — every field, the per-component
sticky semantics, the pool fill/maintain rules and tuning tips.

---

## 4. MASQUE outbound — Cloudflare WARP (SPEC 021)

A `masque` outbound tunnels **whole IP packets** over an HTTP/3 or HTTP/2 connection using
**CONNECT-IP (RFC 9484)**, primarily to connect to **Cloudflare WARP**. (This is CONNECT-IP,
not CONNECT-UDP/RFC 9298; and unrelated to the AWG `id/ip/ib` *masquerade* sugar in §2 — same
word, different feature.) The core builds a userspace gVisor stack per tunnel and routes traffic
through it like a WireGuard endpoint. Gated on `with_quic` + `with_gvisor` (both in the default
`LX_TAGS`). Key material is taken ready from config — device registration (ECDSA keys, WARP
enroll) is done by the client, not the core.

> ⚠️ **The HTTP version is `vhttp`, not `network`** (SPEC 062). Older configs used `network`
> for `h3`/`h2` — the opposite of what `network` means on every other outbound. The old shape
> still works and reports a deprecation; see the migration table below.

The required fields are `server`/`server_port`, the key pair (`private_key`/`public_key`,
for the default `cloudflare` profile) and at least one of `ip`/`ipv6` (your local address
*inside* the tunnel, not the exit IP). Everything else has a default: `profile: cloudflare`,
`vhttp: auto`, `tls.server_name: www.cloudflare.com`, `mtu: 1280`, `idle_timeout` off,
`keep_alive_period: 30s`, `network_list: tcp+udp`. TLS goes in the standard outbound `tls`
block.

> **The default SNI is `www.cloudflare.com`, not the endpoint hostname** — naming the
> MASQUE endpoint in the ClientHello is exactly what a DPI filters on. The endpoint is
> authenticated by pinning `public_key`, so the SNI is free to differ.

> **📖 The full field reference — every field with its type and default, the profile
> matrix (`cloudflare` vs `standard`), key-material format, `vhttp` h3-vs-h2 guidance,
> idle-suspend/keepalive behaviour, start-time validation, the pre-SPEC-062 migration
> table and common footguns — is in
> [lx-protocols-transports.md §3](lx-protocols-transports.md#3-masque-outbound-connect-ip--warp)**
> ([RU](lx-protocols-transports.ru.md#3-masque-outbound-connect-ip--warp)).

### Example — WARP (defaults: `vhttp: auto`)

```jsonc
{
  "type": "masque",
  "tag": "warp",
  "server": "162.159.198.2",
  "server_port": 443,
  "profile": "cloudflare",
  "tls": {
    "server_name": "www.microsoft.com"   // any neutral high-traffic host (domain-fronting)
  },
  "private_key": "<base64 DER EC private key>",
  "public_key":  "<base64 DER PKIX public key>",
  "ip":   "172.16.0.2/32",
  "ipv6": "2606:4700:110:...::/128",
  "mtu":  1280
}
```

`"vhttp": "auto"` — **the default** — tries h3 first and falls back to h2 if the QUIC handshake
does not complete within 3 s, remembering the winning mode for the rest of the process. The failure
mode it exists for is the endpoint (or a TCP-only hop in front of it — an HTTP CONNECT `detour`, a
VLESS/Trojan link in a chain) **silently ignoring QUIC** — there is no error to see, only a hang;
measured in the field through a proxied hop where Cloudflare answered TCP:443 but never replied to
QUIC (SPEC 074). On the `standard` profile there is no h2 leg, so `auto` quietly means h3 there
(an explicit `"vhttp": "auto"` on `standard` logs a warning).

For `h2` (CONNECT-IP over TCP:443), change one field: `"vhttp": "h2"`. The `h2` path runs its
TLS through the shared `common/tls` layer, so it gets ClientHello fragmentation like any other
TLS outbound — including the automatic one under `detour`
([§8](#8-automatic-clienthello-fragmentation-under-detour-spec-060)). `h3` is untouched by that:
QUIC does not carry TLS over TCP at all.

> A top-level `dns` block is required — the userspace stack works at L3 and does not resolve
> domains itself; the outbound resolves them via the DNS router before dialing.

> **h3 vs h2 — which to use.** `h3` (QUIC) is the default and fastest. But on networks that
> filter inbound UDP:443, the QUIC handshake hangs and `h3` never comes up — switch that node to
> `h2` (TCP:443), which is device-verified to work there. Also note the first `h3` dial is slow
> (cold CONNECT-IP setup: QUIC handshake + Extended CONNECT + route advertise + stack), so a
> short urltest timeout may mark a fresh h3 node `-1` on the first probe though it works after.

> **Status.** Device-verified end-to-end on real Wi-Fi and LTE — `warp=on`, real traffic on both
> `h3` and `h2`, idle-suspend + self-healing reconnect confirmed on-device.

**📖 [Full reference →](lx-protocols-transports.md#3-masque-outbound-connect-ip--warp)**
([RU](lx-protocols-transports.ru.md#3-masque-outbound-connect-ip--warp)) — complete parameter
table, profile matrix, key-material format, start-time validation and common footguns.

---

## 5. DNS server group (SPEC 033/035)

Several DNS servers behind one tag with a selection strategy. Solves the
"one dead DNS server kills resolution" problem: upstream's `dns.final` is a
*default*, not a fallback, and a rule routing to a server fails the query
outright on any network error, timeout or SERVFAIL. No build tag — the type
is always available; a config without a `group` server behaves exactly like
upstream.

Servers carry **no states** (no down/backoff). There are two TTL'd record
tables instead: an **error** record (any failed exchange; erases the
server's live wins) and a **win** record (only the first success of a
fan-out; any success erases the server's live errors). **Clean** = zero
live errors. A network change amnesties both tables.

### Fields (a `dns.servers[]` entry)

```jsonc
{
  "type": "group",                  // selector — must be "group"
  "tag": "public",
  "servers": ["google", "cloudflare", "quad9"], // REQUIRED, ≥1 tags.
                                    //   Order is NOT meaningful in any mode
  "mode": "stable",                 // stable (default) | fastest | parallel
  "error_ttl": "2m",                // default 2m: how long an error record lives
  "win_ttl": "5m"                   // default 5m: how long a win record lives.
                                    //   fastest only; ignored elsewhere (warning)
}
```

**Failure** = transport error, timeout, or `SERVFAIL`. `NXDOMAIN` and empty
answers are **valid responses** (and competitive wins when first in a fan).

**Modes** (target chosen among the clean; with **no clean member** every
mode makes exactly ONE attempt via the least dirty server and never fans —
the anti-storm "survival" rule):

- `stable` — stickiness before randomness: stay on the current server while
  it is clean; re-elect a random clean one only when it is not. No
  return-to-primary: a recovered ex-target just rejoins the pool.
- `fastest` — the clean server with the most live wins; when nobody has a
  live win, the query becomes an **election fan** to all clean members
  (single-flight: one election at a time, concurrent queries go to a random
  clean member). Re-election rhythm = `win_ttl` expiry.
- `parallel` — every query fans to all clean members; no wins recorded;
  N× traffic by definition.

**Unified flow:** the single target gets a sub-deadline of HALF the
remaining request budget — the rescue fan is guaranteed the rest. On target
failure the query fans to the remaining clean members; the first success
answers (and becomes the sticky target), stragglers are discarded (never
cached, but their success still heals their server's error records). A fan
failure observed after the request context ended is an artifact, recorded
nowhere.

**Observability:** the DNS query stream reports the member that actually
answered (cache hits and total failures keep the group tag), the probe
trace (group path inside-out, attempts with outcome
`answered`/`timeout`/`network_error`/`servfail` and rtt), and the `fanned`
/ `survival` flags. `GetDNSGroups` (§7, `with_lx_command`) returns the live
records: per member — clean, live errors (count + age of newest), live
wins, last rtt, current flag.

> **Name leak warning.** Any mode fans the query name to all clean members
> on a failure; `parallel` does it on every query. Do not mix internal and
> public resolvers in one group.

### Example — resilient public DNS as the default

```jsonc
{
  "dns": {
    "servers": [
      { "type": "udp", "tag": "google",     "server": "8.8.8.8" },
      { "type": "udp", "tag": "cloudflare", "server": "1.1.1.1" },
      { "type": "group", "tag": "public",
        "servers": ["google", "cloudflare"],
        "mode": "fastest", "error_ttl": "2m", "win_ttl": "5m" }
    ],
    "final": "public"
  }
}
```

> ⚠️ The v1 contract (`mode: failover|race`, `interval`, `down_time`,
> shipped in `v1.14.0-lx.16-rc.1`) is GONE: such configs fail to load.

## 6. VLESS `encryption` — post-quantum layer (SPEC 032)

A flat `encryption` field on a `vless` outbound enables the `mlkem768x25519plus`
handshake **inside** VLESS — above the transport/TLS, below the VLESS client, and
independent of REALITY's key exchange (different layer, do not confuse them).
Servers that mandate it (Xray `decryption` configured) silently drop plain VLESS:
the transport comes up (WS `101`, gRPC SETTINGS answered) and the peer then tears
the connection down without a line in the core log — that is the symptom this
field cures. Client half only; the server half is deliberately not ported
(client-focused fork). Always built, no build tag.

### Field (on a `vless` outbound)

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `encryption` | string | `""` | `""`/`"none"` = layer off (upstream behavior, byte for byte). Otherwise a spec string, validated at `check`/start with segment-precise errors |

Spec-string grammar (dot-separated):

```
mlkem768x25519plus.<native|xorpub|random>.<0rtt|1rtt>[.<padding>…].<key>[.<key>…]
```

- **appearance** — how the layer looks on the wire: `native` (AEAD headers shaped
  like TLSv1.3), `xorpub` (XOR-ed by public key), `random`.
- **rtt** — `0rtt` or `1rtt`.
- **padding** — optional short blocks like `100-111-1111` (a segment shorter than
  20 chars before the first key is read as padding).
- **key** — base64url of an X25519 (32-byte) or ML-KEM-768 (1184-byte) public
  key; several keys may be listed. A working ML-KEM-768 key is ~1579 chars — a
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

> **Status.** Shipped in `v1.14.0-lx.18`, **device-verified** on the subscription
> that prompted it: +10 previously dead nodes (6/8 WS, 4/4 gRPC), no other
> transport group moved. The `native.0rtt` form is the field-proven one;
> `1rtt`+padding and `xorpub`/`random` parse and build but have not met a live
> server yet. In a subscription the value arrives as
> `settings.vnext[0].users[0].encryption` — on the sing-box outbound it is a flat
> `encryption` field beside `uuid`; a config builder that drops it leaves the
> core with nothing to act on.

## 7. Observability (CommandClient extensions)

These are **client-API additions, not config** — extra methods on libbox's `CommandClient`
(the native gRPC management channel), all gated behind `with_lx_command` and consumed by
LxBox. They add nothing to a sing-box config file; you enable them by building with the tag
and calling them from the client. Without the tag the methods are absent and the daemon
serves only the upstream command set.

The added `CommandClient` methods:

- **`URLTestOutbound(tag, link, timeout)`** — synchronously measure the latency of a single
  outbound **or endpoint** on demand (not just a group's periodic test).
- **`GetRules()`** — pull a snapshot of the routing rule table (route rules + DNS rules).
- **`GetGroups()`** — pull a snapshot of the outbound groups (same data the group stream
  pushes).
- **`GetOutbounds()`** — pull the flat outbound/endpoint list (needed alongside `GetGroups`
  because standalone outbounds are not in any group).
- **`GetPool(groupTag)`** — read a `urltest` group's current round_robin rotation pool, slot
  by slot (SPEC 019; see [§3](#3-round_robin-load-balancing-spec-019)).
- **`GetDNSGroups()`** — the live state of every DNS `group` server (SPEC 035; see
  [§5](#5-dns-server-group-spec-033035)): per member `clean` / `liveErrors` /
  `lastErrorAgeMs` / `liveWins` / `current`.
- **`GetRunningConfig()`** — the canonical JSON options the running box was actually built
  from, post-override (SPEC 037). Returned as an object with a `Content()` accessor — a bare
  `string` return would crash gomobile on android/arm64 (SPEC 038).
- **`SubscribeDNSQueries(includeAnswers, handler)`** — a structured live DNS-query stream
  (SPEC 018): per query the `domain`, `qtype`, `rcode` (**`-1` = lookup failure**, a
  first-class state), the CNAME chain / answers (when `includeAnswers`), process attribution,
  and `dnsServer` / `dnsServerType` / `outbound` (an empty `outbound` means direct/system —
  a valid state, not a bug).
- **`GetChains()`** — the state of every `chain` outbound (SPEC 073; see [§9](#9-chain-outbound--a-virtual-multi-hop-path-of-groups-and-nodes-spec-073)):
  per position the resolved node and, for positions ≥ 1, the link instance (`starting|active|idle`,
  live connections, effective MTU and why, what `strip` removed, `rewrite` applied, last error),
  plus dial/error/link counters.

SPEC 017 also enriches the existing connection stream: a tracked `Connection` now carries a
separate **`detourList`** field — the transport-detour tail of the final outbound, exposed
distinctly from the proxy `chain` (the chain omits the detour by design).

Build with the tag to get them:

```sh
make -f Makefile.lx lx-build   # includes with_lx_command (and with_xhttp/with_awg)
```

---

## 8. Automatic ClientHello fragmentation under `detour` (SPEC 060)

**Not a config key — a changed default.** When a TLS-over-TCP outbound (VLESS, trojan, vmess,
anytls, shadowtls, http, masque `h2`, …) dials **through `detour`**, `record_fragment` now
defaults to **on**.

Why: the lower leg forwards our ClientHello under its own name, and the path MTU beyond that
server can be lower than the ClientHello. The ICMP *Fragmentation Needed* never reaches us, so
the packet simply vanishes and the connection dies as `tls handshake: EOF` after 12–17 s. It
reproduces with a bare `curl`, so it is a property of the path, not of sing-box — but only
fragmenting the first TLS record works around it. Measured through a broken leg: no
fragmentation ❌ fail in 12 s; `fragment` ✅ 0.6 s; `record_fragment` ✅ **0.1 s**.

Rules:

- **An explicit config value always wins.** `"fragment": true` is not upgraded to record-split —
  choosing packet-split stays your choice.
- **Only the handshake is fragmented**, never the traffic after it. There is no ongoing cost.
- **`h3`/QUIC is untouched** — no TLS over TCP there, and quic-go keeps its Initial below the
  threshold anyway (masque `h3` through detour: 4/4 OK).
- Nested chains are covered automatically: every link carries its own `detour`.

> ⚠️ **Known limit:** an explicit `"record_fragment": false` is indistinguishable from "unset",
> so auto still turns it on under `detour`. To dial through a detour with a different mode, set
> `"fragment": true`; there is currently no way to ask for no fragmentation at all there.

---

## 9. `chain` outbound — a virtual multi-hop path of groups and nodes (SPEC 073)

Build tag `with_lx_chain` (in the desktop `LX_TAGS` and the AAR). Without it
`"type": "chain"` is rejected at load time.

```json
{
  "type": "chain",
  "tag": "virtualisation",
  "outbounds": ["selector-in", "selector-mid", "selector-exit"],
  "idle_timeout": "5m",
  "strip_evasion": true,
  "strip": { "multiplex.padding": false, "tls.utls": true },
  "rewrite": { "wireguard": { "mtu": 1200 } }
}
```

**Order = packet order.** `outbounds[0]` is the first hop from the client (it touches the
real network and is used as is, dial fields included); the last entry is the node whose
address the destination sees. This is the *opposite* of how a `detour` chain reads
(there the node carrying `detour` is the exit). Any position may be a node, an endpoint
or a group of any nesting — all three shapes behave the same and the length is free (≥ 2):

```
["selector-in", "selector-mid", "selector-exit"]   // all groups
["node-in",     "node-mid",     "node-exit"]       // all nodes
["node-in",     "selector-mid", "node-exit"]       // mixed
```

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `outbounds` | list of tags, ≥ 2, all distinct | required | positions in packet order; a repeated tag is a start error — see below |
| `idle_timeout` | duration | `5m` | an idle link instance (see below) with zero live connections is removed after this; `0` = keep until stop |
| `strip_evasion` | bool | `true` | remove one-sided DPI tricks from links at positions ≥ 1 (catalog below) |
| `strip` | map key → bool | `{}` | patch over the catalog: `false` keep, `true` strip additionally; unknown key = start error |
| `rewrite` | map type → JSON object | `{}` | merge-patch (RFC 7396) applied to the config of every link of that type at positions ≥ 1 |

**One node at several positions.** Tags must be distinct: a repeat fails the start with
`duplicate outbound in chain`, because in a list read as a path it is almost always a typo.
To place the same node at several positions on purpose — the "sandwich" `WARP → someone
else's node → WARP`, where the outer layers hide the middle node from your address and the
destination from the middle node — declare it once per position under different tags; the
declarations may carry identical credentials. Each position gets its own link with its own
detour (a link is keyed by position and node), so the two instances are independent.

How it works: **groups are never copied** — the chain calls the original group and lets it
pick with all its logic (manual selection, health check, sticky, penalties,
`interrupt_exist_connections`). A node picked at position ≥ 1 is served by its **link** —
a runtime instance of that node that dials its server through the previous position. Links
are created on first use (plus a warm-up at start for positions whose pick is known: nodes
and selectors; urltest positions stay lazy) and share the node's tag, so the group's
history and penalties keep working. A link is removed only when it has **no live
connections and was not picked for `idle_timeout`**; switching a group does not kill the
old link while a stream still runs through it. WireGuard links obey idle-suspend like any
endpoint.

- **`direct` at position ≥ 1 is transparent** — "no hop here"; put `direct` into a selector
  to switch a position off at runtime. `block` rejects. All-`direct` positions ≥ 1 make the
  chain equal to position 0.
- **MTU of tunnel links (WireGuard, MASQUE) is lowered automatically**: `mtu` in the node's
  config means "as standalone"; the chain subtracts the exact encapsulation overhead of
  IP tunnels *below* the link (WG inside an IP tunnel −60/−80 by the server address family,
  MASQUE ≈ −90), taking the worst case over a group's members. Over stream proxies
  (vless/trojan/ss over TCP, mux) and datagram proxies the MTU is left as configured.
- **`strip` catalog** (one-sided, the server never sees them): `tls.fragment` (packet-level
  ClientHello fragmentation + `fragment_fallback_delay`; **`record_fragment` is not
  touched** — under `detour` it switches on automatically as a path fix, see §8),
  `multiplex.padding`, `xhttp.padding` (minimal range, obfs mode off). `tls.utls` is
  available via `"tls.utls": true` (start error on a node that uses `reality`). Server
  contracts — `flow`, `obfs`, `shadowtls`, `plugin`, `udp_over_tcp`, `ech`, transport
  paths — are never stripped.
- Order of transformations for a link: `strip` → `rewrite` → MTU. All patches are dry-run
  at start against every node reachable at positions ≥ 1 — a `rewrite` with an unknown
  field fails the start, not the first dial.

> ⚠️ **DPI between hops.** The default strips ClientHello fragmentation at positions ≥ 1
> because the usual DPI sits between you and the first hop. With a domestic relay as
> position 0 and the DPI at the border (`["relay", "foreign-node"]`) the fragmentation is
> needed at position 1 — set `"strip": {"tls.fragment": false}`.

Observability: a connection's `detourList` (§7) shows the resolved path; `GetChains`
(CommandClient) / Clash API `/proxies/<tag>` → `chain` give the per-position state (picked
node, link state `starting|active|idle`, live connections, effective MTU and why, what was
stripped/rewritten, last error) and counters. Dial errors name the position and the hop
below: `chain[virtualisation] #2 (warp-exit) via #1 (node-m): …`. Per-layer latency:
URLTest the internal hop tags `<tag>#0`, `<tag>#1`, … — each measures the path up to that
position. Known limits: a group at position ≥ 1 ranks its nodes by their *direct*
measurements; a tunnel above a hop without UDP fails at dial time with both positions
named; nested `chain` is allowed only at position 0.

---

## 10. Validate & build

```sh
git clone --recurse-submodules <repo>           # with_awg needs the submodule
make -f Makefile.lx lx-build                     # builds ./sing-box with both features
./sing-box check -c lx-test/config/xhttp_reality.json
./sing-box check -c lx-test/config/awg2_basic.json

# Android (optional): libbox.aar with with_xhttp+with_awg baked in (needs NDK r28 + OpenJDK 17)
make lib_install && make lib_android             # → libbox.aar (SDK23) + libbox-legacy.aar (SDK21)
```

The CI (`.github/workflows/lx-ci.yml`) builds the feature matrix (`baseline` / `xhttp` / `awg` / `full`), a cross-platform matrix, **and the Android `libbox.aar`** (gomobile), running `check` on the matching sample configs. Pushing a `v*-lx.*` tag runs `lx-release.yml`, which publishes the desktop binaries **and** `libbox-<ver>.aar` / `libbox-legacy-<ver>.aar` as GitHub Release assets. A **Windows 7 (32-bit)** legacy binary (`sing-box-<ver>-windows-386-legacy-windows-7.zip`) is also published — built with a Win7-patched Go and **without `with_naive_outbound`** (`cronet-go` has no windows/386 build; every other feature is unchanged).
