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
  - [3.1 Common fields](#31-common-fields)
  - [3.2 least_test — fastest node](#32-least_test--fastest-node)
  - [3.3 round_robin — pool with stickiness](#33-round_robin--pool-with-stickiness)
  - [3.4 failover — hold until it fails](#34-failover--hold-until-it-fails)
  - [3.5 Choosing a mode](#35-choosing-a-mode)
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
- [13. The `lx` root block](#13-the-lx-root-block)
- [14. Tailscale: turning Taildrop off](#14-tailscale-turning-taildrop-off)

---

## Features and build tags

`sing-box-lx` is upstream [sing-box](https://github.com/SagerNet/sing-box) plus a small set of client-side features, each gated behind a build tag:

| Feature | Build tag | Where it lives in config | Included in |
|---------|-----------|--------------------------|-------------|
| **XHTTP** transport (Xray-compatible) | `with_xhttp` | `transport.type: "xhttp"` on a VLESS / VMess / Trojan outbound | desktop + mobile |
| **AmneziaWG 2.0/3.x** (AWG2, AWG3) | `with_awg` | extra fields on a `wireguard` endpoint | desktop + mobile |
| **MASQUE** outbound (CONNECT-IP / WARP) | `with_quic`+`with_gvisor` | `outbounds[].type: "masque"` | desktop + mobile |
| **TrustTunnel** outbound and inbound | `with_trusttunnel` (+ `with_quic` for HTTP/3) | `outbounds[].type` / `inbounds[].type: "trusttunnel"`; fields — [protocols-transports.md §6](protocols-transports.md#6-trusttunnel-outbound-and-inbound), the global `lx.trusttunnel.idle_timeout` — [§13](#lxtrusttunnel--trusttunnel-outbounds) | desktop + mobile |
| **Idle-suspend** | `with_lx_idle_suspend` | `lx.wg.idle.suspend` (+ `suspend_reachable`, `teardown`) | mobile only (AAR) |
| **Root `lx` block** | — (always parsed) | `lx.wg`, `lx.masque`, `lx.trusttunnel`, `lx.mtu_align`, `lx.sniff_guard` — the fork's global knobs, [§13](#13-the-lx-root-block) | desktop + mobile |
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
desktop binary refusing them are in [§13](#lxwg--wireguard--amneziawg-endpoints); the energy
model and the recommended mobile configuration are in [lx-energy.md](lx-energy.md).

> ⚠️ All keys/UUIDs below are placeholders. Never commit real private keys / pre-shared keys to a repository.

---

## 0. Every field at a glance (exhaustive example)

One config carries every field of the outbound-side features: XHTTP transport,
AmneziaWG 2.0 endpoint, the `id`/`ip`/`ib` masquerade sugar, VLESS `encryption`, the
`urltest` `round_robin` balancer and the root `lx` block with every key. MASQUE and the DNS group have
their own examples in [§4](#4-masque-outbound--cloudflare-warp) and [§5](#5-dns-server-group).
The `lx` block is described in [§13](#13-the-lx-root-block), its energy keys in [lx-energy.md](lx-energy.md).

This is a kitchen-sink reference, not a recommended config. Many fields are mutually exclusive
(the `id`/`ip`/`ib` sugar and a hand-written `i1`) or server-only and ignored by the client;
those fields are flagged in the comments. For a working setup, copy only the block
you need and read its section below. Each comment shows the default and the allowed values.

```jsonc
{
  // ─────────────────────────────────────────────────────────────────────────
  // Root `lx` block (§13) — the fork's global knobs, grouped by subsystem.
  // lx.wg.idle acts only with the with_lx_idle_suspend build tag (mobile AAR);
  // a desktop binary refuses it at start. The rest of lx.wg works in every build.
  // ─────────────────────────────────────────────────────────────────────────
  "lx": {
    "wg": {
      "idle": {                                 // with_lx_idle_suspend builds only
        "suspend": "30s",                       // default: off. Endpoint outside the routing tree → Down after this idle
        "suspend_reachable": "5m",              // default: off. Same for endpoints in the tree; >= suspend, requires it
        "teardown": "10m",                      // default: = suspend_reachable. Sleep before full teardown; "0" = never
        "lazy_build": true,                     // default: false. Build the device on first dial (SPEC 097)
        "build_max": 4,                         // default: 0 (no cap). Max devices built at once (SPEC 097)
        "build_overflow": "wait"                // default: wait. wait | build (SPEC 097)
      },
      "buffers": {                              // any build (SPEC 158)
        "read_datagram_count": 8,               // default: 128. 1–128 datagrams reserved per socket read
        "send_by_mtu": true                     // default: true. false = 64 KiB stack-read buffers (diagnostics)
      },
      "connected_socket": true                  // default: false. Connect the tunnel socket in single-peer mode (SPEC 160)
    },
    "masque": {
      "idle_timeout": "5m"                      // default: off. Global idle window for masque nodes without their own key
    },
    "trusttunnel": {
      "idle_timeout": "5m"                      // default: 5m. Global idle window for trusttunnel nodes without their own key; only a node's "0" turns it off (SPEC 133)
    },
    "mtu_align": "clamp",                       // default: clamp. off | fill | clamp, or an object {"mode", "except": [tags]} (§13)
    "sniff_guard": {                            // default: no block = upstream behaviour. Guards the handshake against rerouting by SNI on a relay or router (§13, SPEC 060)
      "nodes": {
        "sni_mismatch": true,                   // default: true — only nodes whose SNI differs from server. false — no filter
        "outbound_type": ["vless", "masque"]    // default: no filter. A list of types or false; true, [] and an unknown type are load errors
      },
      "detour_type": {                          // default: {"direct": "clean", "ssh": "clean", "socks": "clean", "*": "suspect"}
        "direct": "clean",                      //   carrier type → never | clean | suspect; "*" — every other type; groups cannot be keys
        "*": "suspect"
      },
      "override": false,                        // default: false. true — the block wins over a node's fragment / record_fragment
      "tls": {
        "method": "record_fragment",            // default: off. record_fragment | fragment | delay | off
        "delay": "300ms",                       // default: 300ms. Pause of the delay method; > 0
        "persist": true,                        // default: false. Relay labels in experimental.cache_file
        "detour_type": { "ssh": "suspect" }     // default: the shared map. Override for TLS
      },
      "quic": {
        "method": "decoy_first_byte",           // default: off. decoy_first_byte | delay | off
        "delay": "300ms",                       // default: 300ms
        "persist": true,                        // default: false
        "detour_type": { "socks": "never" }     // default: the shared map. Override for QUIC
      }
    }
  },

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
        "reality": {
          "enabled": true, "public_key": "<reality-public-key-base64>", "short_id": "0123abcd",
          "key_share": ""                       // default: "" (as the fingerprint carries it). §7:
                                                //   "classical" = strip X25519MLKEM768 (Xray < v26.9.8 only)
                                                //   "hybrid"    = require it (error on edge/ios/…)
        }
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

      // ── AmneziaWG 3.x (amnezia-awg2 container, protocol_version 3.x; SPEC 080) ──
      // header_protection_key is SERVER-SIDE (must equal the server's); with it on,
      // every s1..s4 must be >= 12 (the padding carries the header-cipher nonce).
      "header_protection_key": "<base64-32-bytes>", // default: "" (off). `awg genkey` output; masks type/receiver/counter of every packet
      "content_padding_addition": "10-100",     // default: "" (off). int | "min-max" extra padding inside every data packet
      "rekey_after_time": "100-120",            // default: "" (WireGuard 120 s). int | "min-max" seconds
      "rekey_timeout": "3-7",                   // default: "" (WireGuard 5 s)
      "reject_after_time": "150-180",           // default: "" (WireGuard 180 s)
      "keepalive_timeout": "5-15",              // default: "" (WireGuard 10 s)
      "max_handshake_attempts": "15-20",        // default: "" (WireGuard 18)
      "random_trailers": true,                  // default: false. Random-length tail on every handshake message / inside data packets
      "disable_cookies": true,                  // default: false. Never send or demand cookie replies

      "peers": [
        {
          "address": "server.example.com",
          "port": 51821,
          "public_key": "<server-public-key-base64>",
          "pre_shared_key": "<preshared-key-base64>",
          "allowed_ips": ["0.0.0.0/0", "::/0"],
          "persistent_keepalive_interval": "25-35" // number (WireGuard) or "min-max" seconds (AWG 3.x)
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
      "mode": "round_robin",                    // default: least_test. least_test | round_robin | failover
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

Field count: 26 XHTTP + 30 AmneziaWG (incl. `id`/`ip`/`ib` and the 9 AWG 3.x keys) + 1 VLESS (`encryption`) +
5 `urltest` (`mode` + `balancer{pool,pool_tolerance,sticky_hash}`) + 24 `lx` (9 `lx.wg` + `lx.masque.idle_timeout` + `lx.trusttunnel.idle_timeout` + `lx.mtu_align` + 12 `lx.sniff_guard{nodes{sni_mismatch,outbound_type},detour_type,override,tls{method,delay,persist,detour_type},quic{method,delay,persist,detour_type}}`).
Mutually exclusive and ignored fields are flagged in the comments above. The sections below give
the per-field semantics, gotchas and live verification status.

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

### 3.1 Common fields

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `mode` | string | `least_test` | `least_test` — the fastest node, as upstream; `round_robin` — a pool; `failover` — hold the working node until it fails. `least_connection` is a load error: `round_robin` already spreads connections evenly |
| `balancer` | object | — | `round_robin` parameters; with another `mode` it is a startup error. The fields are in [§3.3](#33-round_robin--pool-with-stickiness) |

### 3.2 least_test — fastest node

Every `interval` the group checks every node and moves to another one as soon as it is faster
than the current one by more than `tolerance` ms (default `50`). Upstream and a group without
`mode` work the same way.

The fork adds a reaction to failures. If a connection through a node fails with a "path is
dead" error — a timeout, an unreachable host, a refused connection to a proxy node — the node
gets a penalty, and the connection opens once through the best of the other nodes. If that
works, the group moves to that node, and open connections stay where they are. The penalty is
lifted when the node answers again. For `direct` and tunnels (WireGuard, AWG, Tailscale) a
refused connection counts as the site's answer and gives no penalty.

### 3.3 round_robin — pool with stickiness

`round_robin` spreads connections over a pool of several live nodes. Each `interval` only the
pool is checked, not every node, so the mode suits lists of hundreds of nodes. A node is picked
once per connection; a UDP/QUIC session stays on its node. The upstream `tolerance` field does
not apply here; `pool_tolerance` takes its place. If `tolerance` is set and `pool_tolerance` is
not, a startup warning says so.

#### `balancer` fields

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `pool` | int | `3` | how many nodes the pool holds. `0` or omitted — `3`; negative is an error. The pool holds no more nodes than `outbounds` has |
| `pool_tolerance` | int (ms) | `0` | `0` — the pool is filled with live nodes without comparing delays, and no more nodes are checked than needed; the mode for large lists. `> 0` — every node is checked and the pool keeps the fastest `pool`; a pool node is replaced only when an outside node beats it by more than `pool_tolerance` ms |
| `sticky_hash` | string[] | `["process","domain"]` | what the binding key is built from (see [Slot-hash binding](#slot-hash-binding)): `process`, `domain`, `source_ip`, `dest_ip`, `dest_port`. Omitted or `[]` — the default. Only `["none"]` turns the binding off |

> ⚠️ `"sticky_hash": []` does not turn the binding off. The sing-box config decoder reads an
> empty array as an omitted field, that is, as `["process","domain"]`. `["none"]` turns the
> binding off; `none` together with another component is an error.

A dead pool node keeps its slot until a live replacement is found, so the pool never empties.
While the pool has a live node, new connections skip the dead one: its sticky keys spread evenly
over the live nodes and come back once the node answers again; without the binding the rotation
runs over the live nodes. A quarantined node is skipped the same way. A failed connection starts
the [self-heal](#pool-self-heal).

A node is live or dead by the shared history of its checks. So a manual delay test with a short
timeout that does not complete (Clash API `/proxies/{name}/delay`, `URLTestOutbound`) and a
failed connection of a `least_test` group through the same node also take it out of rotation
until the next pool check (at most `interval` plus the probe time).

#### Slot-hash binding

A flow's key is bound not to a node but to a slot number: `slot[hash(key) % pool]`. When the
node in a slot changes, the replacement takes the same slot. So the keys of the other slots stay
on their nodes, and their connections are not reopened.

By default the key is the process and the destination domain. `domain` is the domain from the
original request, and it stays filled after the router resolves the domain to an IP. Keep
`domain` in the key for domain traffic: `dest_ip` is empty until the resolve, so a key of only
`source_ip`/`dest_ip`/`dest_port` puts every such flow of one source on one slot.

#### Pool self-heal

The pool recovers from a node failure without waiting for `interval`.

The scheduled check walks the nodes once per `interval`, and on a large list that is a long
process: two checks are often 10–15 minutes apart. That is too rare for live traffic: a node that
dies right after a check would keep its share of new connections until the next one. So a
`round_robin` group has recovery mechanisms driven by the connections themselves. A failed node
does not drop connections: one that does not open through the dead node opens through the next
one, and the node is replaced with a live one at once, without waiting for the check. A site
closed to one node moves to another node of the pool, while the rest of the traffic stays on its
nodes by stickiness. When the local network is lost (device sleep, a Wi-Fi switch), the group
strikes out no node, and once the network is back the pool is the same.

There is nothing to configure: the behaviour is always on, with no config keys.

##### What the application sees when a node fails

| What happened | What the group does | What the application sees |
|---|---|---|
| A pool node died between checks: the server process stopped, the host went down | A connection that does not open through the node is retried through the next slot. The node is struck out, and its slot gets a replacement from outside the pool | No failed connection. If the host is down rather than the process, the first connection waits up to 5 s for the failure, then the retry runs |
| A node accepts connections and stays silent | After 5 s new connections skip its slot. After 20 s (the 5 s wait and the 15 s probe timeout) the node is struck out and replaced. Connections still opening through it are retried through another node; open connections still waiting for a first byte are closed | Connections that reached the node in its first 5 s wait until it is struck out or the application gives up; the application repeats a closed one |
| A destination is unreachable through one node: a geo-restriction, the site bans the node's address | After the first failure the group checks that the node itself answers and moves this destination to the next slot. Other destinations stay on their nodes | The first request can fail (see the limits); the next ones go through another node |
| A node answers probes but fails a noticeable share of connections | A node with at least 5 % failures and more than twice the pool median goes to quarantine: its slot is skipped and gets a replacement until the next scheduled check | Fewer failed connections |
| The local network is gone: device sleep, a Wi-Fi or mobile network switch, an ISP outage | Nothing answers, so no node is struck out, and the group freezes for 15 s | The same pool once the network is back |

Where it shows:

- a pool with a rare check (`interval` of 10 minutes or more) — a server that goes down between
  checks does not take its share of connections until the next check, it is replaced at once;
- a site closed to one node (by geography, for example) opens through another and keeps that
  choice;
- phones and laptops — sleep and network switches do not lose the pool.

Six limits:

- Only `mode: round_robin` recovers this way. `least_test` and `failover` keep their penalty and
  fallback dial on "path is dead" errors ([§3.2](#32-least_test--fastest-node)).
- The first request to a destination closed to a node can still fail. Through VLESS, Trojan, VMess
  and Shadowsocks the failure shows only after the connection has opened, so the group does not
  retry it; the application does. Through `direct` and WireGuard such a request waits out two
  timeouts in a row.
- A destination stays on its new slot until the core restarts. The table holds 1024 keys, and the
  oldest drops out when it is full. A destination closed through every pool node returns to its
  own slot until the next check.
- With stickiness off (`sticky_hash: ["none"]`) destinations do not move; dead and quarantined
  nodes are still skipped.
- UDP is covered in part: a dead node is struck out for UDP too, but UDP failures move no keys and
  do not count toward quarantine.
- Each connection costs a little extra work when it opens. Download and upload speed stay at the
  level of a build without self-heal; the difference shows only in a synthetic run of thousands
  of very short connections in a row ([measurements](../SPECS/TASKS/138-URLTEST_RR_SKIP_DEAD_SLOT/IMPLEMENTATION_REPORT.md)).

`sing-box api group pool <tag>` (a build with `with_lx_command`) shows the state: a slot table
with `STATE` (`live`/`dead`/`quarantined`), `CONNECTIONS` and `FAILURES`, plus the moved keys, the
address problems and the freeze:

- `quarantined` — the node fails connections more often than the rest; new connections skip it
  until the next check or network change;
- a moved key — a site closed to its node has moved to the next pool node;
- an address problem — the site did not open through any pool node and went back to its own node
  until the next check or network change;
- the freeze — for 15 s after the network is lost, the device wakes or the network changes, the
  group touches nothing.

The daemon's `GetPool` carries the same: per slot `quarantined`, `connections` and
`confirmedFailures`, per group `shiftedKeys`, `markedKeys`, `frozen` and `frozenSeconds`. In
libbox the group fields come with `GetPoolState` ([§8](#8-observability-commandclient-extensions)).
The connection list shows the node each connection went through, including a retry. The full
rules are in
[SPEC 138 §3](../SPECS/TASKS/138-URLTEST_RR_SKIP_DEAD_SLOT/SPEC.md#3-самовосстановление-по-отказу-соединения).

#### Example — urltest with round_robin

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

For a large node list, `pool_tolerance: 0`, a small `pool` and a longer `interval` give the
fewest checks.

Status: even rotation is verified locally (10/10/10 with stickiness off) and on a device, on a
real multi-node pool. Per-domain uniformity on the device is 0.95+.

📖 [Field reference](../docs/configuration/outbound/urltest.md): every field and the binding key
components.

### 3.4 failover — hold until it fails

**Selection.** The group takes the fastest node at the moment of choice, without `tolerance`;
the order of the `outbounds` list does not matter.

**Hold.** While the node works the group stays on it, even if another one becomes faster. Each
`interval` only that node is checked: one probe instead of checking every node.

**Degradation.** If the node answers more than 2× slower than the fastest other node of the same
network (by its last known delay), it is checked once more at once. If it is still more than 2×
slower, the group checks every node and moves to the fastest only if that node is more than
1.5× faster; otherwise it stays: the network got slower, not the node. No config key.

**Failure.** Two kinds:

- the node does not answer a probe → the group checks every node and moves to the fastest live
  one;
- a connection through the node fails with a "path is dead" error → a penalty and one fallback
  connection, as in [least_test](#32-least_test--fastest-node); the group moves to the fallback
  node, and open connections stay where they are.

After the move the group holds the new node.

**Manual test.** A group test from the UI, CLI (`sing-box api group urltest <tag>`) or Clash API
checks every node and picks the fastest. This is the only way back to the fastest node while the
current one is alive.

**What does not apply.** `tolerance` is ignored, and a startup warning says so. `balancer` is a
startup error.

The UI shows delays of the other nodes as of the last check of every node.

#### Example — urltest with failover

```jsonc
{
  "type": "urltest",
  "tag": "auto",
  "outbounds": ["proxy-a", "proxy-b", "proxy-c", "proxy-d", "proxy-e"],
  "url": "https://www.gstatic.com/generate_204",
  "interval": "15m",
  "mode": "failover"
  // no tolerance: there are no speed-based switches (a non-zero value logs a warning)
  // no balancer: with failover it is a startup error
}
```

When to pick it: holding one working node matters more than always being on the fastest one —
mobile profiles, for example. How many probes this saves is in [lx-energy.md §6](lx-energy.md#6-urltest-probes-wake-sleepers-so-five-mechanisms-bound-them).

### 3.5 Choosing a mode

| What matters more | Mode |
|---|---|
| fewest switches and one probe per `interval` | `failover` |
| spreading load over several nodes | `round_robin` |
| always the fastest node, at the cost of checking every node | `least_test` |

On energy: `least_test` wakes every node each `interval`, `round_robin` only the pool (with
`pool_tolerance: 0`), `failover` one node; details in [lx-energy.md §6](lx-energy.md#6-urltest-probes-wake-sleepers-so-five-mechanisms-bound-them).

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
| `idle_timeout` | off, or the global `lx.masque.idle_timeout` ([§13](#13-the-lx-root-block)); the node's own key wins over the global one, `"0"` included |
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
  `mtu` on their own ([`lx.mtu_align`](#lxmtu_align--path-mtu--packet-size-alignment)): WARP
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

These are client-API additions, not config: methods on libbox's `CommandClient` (the native
gRPC management channel). All of them are gated behind `with_lx_command` and called by LxBox.
They add nothing to a sing-box config file: the methods appear in a build with the tag, and
the client calls them. Without the tag the methods are absent and the daemon serves only the
upstream command set. Details are in the
[OBSERVABILITY feature](../SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md).

The methods the fork adds to `CommandClient`:

- **`URLTestOutbound(tag, link, timeout)`** — synchronously measures the latency of a single
  outbound or endpoint on demand, outside a group's periodic test.
- **`GetRules()`** — returns a snapshot of the routing rule table: route rules and DNS rules.
- **`GetGroups()`** — returns a snapshot of the outbound groups; the group stream pushes the
  same data.
- **`GetOutbounds()`** — returns the flat outbound and endpoint list. It is needed alongside
  `GetGroups` because standalone outbounds are not in any group. A WG/AWG endpoint's item also
  carries `EndpointState` (`never_built` / `building` / `up` / `asleep` / `torn_down` / `down`
  / `disabled`) and `IdleSinceSeconds`, the time since its last dial. Both are empty or 0 for
  other outbounds ([lx-energy.md §11](lx-energy.md#11-lazy-build-and-the-build-budget-keep-devices-from-being-built-for-nothing)).
  The `disabled` state is set by `SetEndpointEnabled` (below;
  [lx-energy.md §12](lx-energy.md#12-a-manual-switch-turns-an-endpoint-off-without-a-config-change)).
- **`GetPool(groupTag)`** — returns a `urltest` group's current round_robin rotation pool,
  slot by slot ([§3](#3-urltest-node-selection-modes)). Per slot it also returns
  `Quarantined`, `Connections` and `ConfirmedFailures`.
- **`GetPoolState(groupTag)`** — returns the same slots plus the group-wide `ShiftedKeys`,
  `MarkedKeys`, `Frozen` and `FrozenSeconds`
  ([self-heal](#pool-self-heal)).
- **`GetDNSGroups()`** — returns the live state of every DNS `group` server
  ([§5](#5-dns-server-group)): per member `clean` / `liveErrors` / `lastErrorAgeMs` /
  `liveWins` / `current`.
- **`GetRunningConfig()`** — returns the canonical JSON options the running box was built
  from, post-override. The result is an object with a `Content()` accessor: a bare `string`
  return would crash gomobile on android/arm64.
- **`SubscribeDNSQueries(includeAnswers, handler)`** — subscribes to a structured live
  DNS-query stream. Per query it delivers the `domain`, `qtype`, `rcode`, the CNAME chain and
  answers (when `includeAnswers`), process attribution, and `dnsServer` / `dnsServerType` /
  `outbound`. An `rcode` of `-1` means a lookup failure, a first-class state. An empty
  `outbound` means direct/system, also a valid state, not a bug.
- **`GetChains()`** — returns the state of every `chain` outbound
  ([§10](#10-chain-outbound--a-virtual-multi-hop-path-of-groups-and-nodes)): per position the
  resolved node and, for positions ≥ 1, the link instance (`starting|active|idle`, live
  connections, effective MTU and why, what `strip` removed, whether `rewrite` applied, last
  error). It also returns dial, error and link counters.
- **`SetEndpointEnabled(tag, enabled)`** — switches a WG/AWG endpoint off or on at runtime
  ([lx-energy.md §12](lx-energy.md#12-a-manual-switch-turns-an-endpoint-off-without-a-config-change)). Returns an
  `EndpointToggleResult` object whose `State` is the endpoint state after the call.

The connection stream is extended too: a tracked `Connection` carries a separate
**`detourList`** field, the transport-detour tail of the final outbound. It is exposed
separately from the proxy `chain`, because the `chain` omits the detour.

The methods appear in a build with the tag:

```sh
make -f Makefile.lx lx-build   # includes with_lx_command (and with_xhttp/with_awg)
```

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

The [`lx.sniff_guard`](#lxsniff_guard--guard-against-rerouting-by-sni) block (§13) turns the
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

The build tag `with_lx_chain` is in the desktop `LX_TAGS` and the AAR. Without it
`"type": "chain"` is rejected at load time. Details are in the
[CHAIN feature](../SPECS/FEATURES/015-CHAIN/FEATURE.md).

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

Positions in `outbounds` follow packet order. `outbounds[0]` is the first hop from the
client: it touches the real network and is used as is, dial fields included. The last entry
is the node whose address the destination sees. A `detour` chain reads the opposite way:
there the node carrying `detour` is the exit. Any position may be a node, an endpoint or a
group of any nesting. All three shapes behave the same, and the length is free (≥ 2):

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

Tags in `outbounds` must be distinct: a repeat fails the start with
`duplicate outbound in chain`, because in a list read as a path it is almost always a typo.
To place the same node at several positions on purpose, declare it once per position under
different tags; the declarations may carry identical credentials. This is how the "sandwich"
`WARP → someone else's node → WARP` is built: the outer layers hide the middle node from your
address and the destination from the middle node. Each position gets its own link with its
own detour (a link is keyed by position and node), so the instances are independent.

Groups are never copied: the chain calls the original group and lets it pick with all its
logic (manual selection, health check, sticky, penalties, `interrupt_exist_connections`). A
node picked at position ≥ 1 works through its link, a runtime instance of that node that dials
its server through the previous position. Links are created on first use. At start, positions
whose pick is known are warmed up — nodes and selectors; urltest positions stay lazy. A link
shares the node's tag, so the group's history and penalties keep working. A link is removed
only when it has no live connections and was not picked for `idle_timeout`. Switching a group
does not kill the old link while a stream still runs through it. WireGuard links obey
idle-suspend like any endpoint.

- **`direct` at position ≥ 1 is transparent**: there is no hop. Put `direct` into a selector
  to switch a position off at runtime. `block` rejects. If every position ≥ 1 is `direct`, the
  chain equals position 0.
- **MTU and packet size of links are aligned automatically** by the shared `lx.mtu_align`
  mechanism ([§13](#lxmtu_align--path-mtu--packet-size-alignment)), the same one that works
  under a plain `detour`. `mtu` in the node's config means "as a standalone node". The chain
  subtracts the exact encapsulation overhead of IP tunnels *below* the link, taking the worst
  case over a group's members: WG inside an IP tunnel −60/−80 by the server address family,
  MASQUE −79/−99. QUIC links (hysteria2, tuic, hysteria) at positions ≥ 1 get an
  `initial_packet_size` that fits the hop below and `disable_path_mtu_discovery: true`. Over
  any proxy (vless/trojan/ss/hysteria2/…) nothing changes. The mode (`clamp` by default,
  `fill`, `off`) and `except` apply to links the same way.
- **`strip` catalog** — one-sided tricks the server never sees:
  - `tls.fragment` — packet-level ClientHello fragmentation and `fragment_fallback_delay`.
    The catalog does not touch `record_fragment`: protection of the path against rerouting
    by SNI is set by `lx.sniff_guard` (§9, §13);
  - `multiplex.padding`;
  - `xhttp.padding` — `x_padding_bytes` is pinned to the lower bound of the node's range, or
    to `100` when no range is set; placement and obfs mode are kept;
  - `tls.utls` — only via an explicit `"tls.utls": true`; on a node that uses `reality` it is
    a start error.
- Server contracts — `flow`, `obfs`, `shadowtls`, `plugin`, `udp_over_tcp`, `ech`, transport
  paths — are never stripped.
- Order of transformations for a link: `strip` → `rewrite` → MTU. All patches are dry-run at
  start against every node reachable at positions ≥ 1: a `rewrite` with an unknown field
  fails the start, not the first dial.

> ⚠️ By default the chain strips ClientHello fragmentation at positions ≥ 1, because the usual
> DPI sits between you and the first hop. With a domestic relay as position 0 and the DPI at
> the border (`["relay", "foreign-node"]`), the fragmentation is needed at position 1. For
> that, set `"strip": {"tls.fragment": false}`.

A connection's `detourList` ([§8](#8-observability-commandclient-extensions)) shows the
resolved path. `GetChains` (CommandClient) and Clash API `/proxies/<tag>` → `chain` return the
per-position state and counters. The state of a position is the picked node, the link state
`starting|active|idle`, live connections, effective MTU and why, what was stripped or
rewritten, and the last error. Dial errors name the position and the hop below:
`chain[virtualisation] #2 (warp-exit) via #1 (node-m): …`. Per-layer latency is measured by
URLTest on the internal hop tags `<tag>#0`, `<tag>#1`, …; each tag measures the path up to its
position.

Known limits: a group at position ≥ 1 ranks its nodes by their *direct* measurements; a
tunnel above a hop without UDP fails at dial time, and the error names both positions; nested
`chain` is allowed only at position 0.

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

```jsonc
"lx": {
  "wg": {
    "idle": {
      "suspend": "30s",
      "suspend_reachable": "5m",
      "teardown": "10m",
      "lazy_build": true,
      "build_max": 4,
      "build_overflow": "wait"
    },
    "buffers": {
      "read_datagram_count": 8,
      "send_by_mtu": true
    },
    "connected_socket": true
  },
  "masque": {
    "idle_timeout": "5m"
  },
  "trusttunnel": {
    "idle_timeout": "5m"
  },
  "mtu_align": "clamp",
  "sniff_guard": {
    "tls": { "method": "record_fragment" },
    "quic": { "method": "decoy_first_byte" }
  }
}
```

### `lx.wg` — WireGuard / AmneziaWG endpoints

The `idle` sub-block works only in the mobile build (`with_lx_idle_suspend`); the other `lx.wg`
keys work in every build.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `idle.suspend` | duration | off | Idle threshold for an endpoint outside the active routing tree: past it the device goes Down (sockets closed, receive buffers freed), the next dial wakes it. Absent or `0` turns the mechanism off; the idle tick does not start |
| `idle.suspend_reachable` | duration | off | Second, longer threshold for endpoints in the tree (urltest pool member, selected node, final); without it they never sleep. Must be ≥ `idle.suspend` and requires it |
| `idle.teardown` | duration, explicit `"0"` allowed | inherits `idle.suspend_reachable` | How long an already-sleeping endpoint sleeps before its device and netstack are torn down, counted from falling asleep. `"0"` disables teardown. Requires `idle.suspend` |
| `idle.lazy_build` | bool | `false` | Endpoints start torn down; the device is built on the first dial. Requires `idle.suspend` |
| `idle.build_max` | integer ≥ 0 | `0` = no cap | At most N devices built at once |
| `idle.build_overflow` | `wait` \| `build` | `wait` | When all `build_max` devices carry live connections: wait within the dial deadline, or build above the cap with a warning |
| `buffers.read_datagram_count` | integer 1–128 | `128`, as upstream | How many datagrams are reserved per socket read. `8` for weak machines. Linux and Android, tunnels without `detour` |
| `buffers.send_by_mtu` | bool | `true` | Size the buffers read from the stack by the tunnel MTU instead of 64 KiB. `false` is for diagnostics |
| `connected_socket` | bool | `false` | Connect the tunnel socket to the server address in single-peer mode. Linux and Android |

Timelines for the three idle keys and the recommended mobile configuration are in
[lx-energy.md](lx-energy.md). The victim order and the states for `lazy_build`, `build_max` and
`build_overflow` are in
[lx-energy.md §11](lx-energy.md#11-lazy-build-and-the-build-budget-keep-devices-from-being-built-for-nothing). How these three
keys behave:

- `lazy_build` does not apply to an endpoint with `listen_port` (nothing dials it).
- `build_max` without `lazy_build` is legal: devices are built at start, and the cap applies
  from the first rebuild (a wake after `idle.teardown`).
- The `wait` of `build_overflow` lasts until the dial's own deadline, at most 15 s.
- A `build_max` below the number of nodes used at the same time makes them tear each other down
  on every switch, each dial paying a device build. Set it for probe-heavy sessions, not as a
  general memory knob.

`buffers` and `connected_socket` are about the socket of a tunnel without `detour`; a tunnel
through `detour` has no socket of its own and is not affected.

- `buffers.read_datagram_count`: a tunnel keeps one 64 KiB buffer per datagram it may read at
  once, in each of its two receive routines (IPv4 and IPv6), for as long as it is up: 16.4 MiB
  at the default 128, about 1 MiB at 8. Below 128 the socket is created without kernel receive
  coalescing (UDP GRO); send coalescing (UDP GSO) stays. On a 4-core Cortex-A55 board between
  100 and 150 Mbit/s the value made no difference to speed or CPU. On a real NIC and on faster
  links the batch and GRO may matter, so the default stays as upstream. Accepted and ignored on
  macOS and Windows.
  Measured with 100 idle tunnels without `detour` in one instance (the same board):

  | `read_datagram_count` | Go heap | Physically used (RSS) |
  |---|---:|---:|
  | 128 (default) | 1740 MiB | 94 MiB |
  | 8 | 138 MiB | 77 MiB |
  | 2 | 58 MiB | 75 MiB |
  | 1 | 44 MiB | 74 MiB |

  An idle tunnel costs little physical memory at any value: a buffer that was never read into
  is not backed by pages. The heap still matters. It counts toward a runtime memory limit, it
  moves the garbage collection threshold (with 1.74 GiB live the next collection is at
  3.5 GiB), and a buffer becomes physical once the tunnel carries traffic. So the more
  tunnels, the smaller the value: `8` for a tunnel or two on a weak machine, `1` or `2` for
  tens and hundreds. Speed at `1` is not measured on the release build; a measurement build
  showed 3 % less upload than at `8`.
- `buffers.send_by_mtu: false` sets the buffers read from the tunnel stack to 64 KiB each, as in
  versions before lx.36. It exists to rule the MTU-sized buffers out when a problem is being
  narrowed down. There is no reason to set it otherwise.
- `connected_socket` applies to a tunnel with one peer, a fixed server address and no
  `listen_port`. The socket is connected to the server, so the kernel keeps the route instead
  of looking it up per datagram. On the same board that gives about 5 % more upload, 7 % together
  with `buffers.read_datagram_count: 8`; measured over loopback. The price: a connected socket
  hears only the server's address, and its source address is fixed at connect time. The core
  disconnects it whenever a send fails or a handshake goes unanswered, then connects again. A
  server that answers from a new address is heard after the next handshake retry, not at once.
  The key is off by default because it is not verified on a phone that changes networks. It
  applies on Linux and Android; macOS connects in this mode by itself.

The keys of `lx.wg.idle` act only in builds with `with_lx_idle_suspend` (the mobile AAR). A
desktop/CLI binary refuses them at start — any idle window, an explicit `idle.teardown` (`"0"`
included), `lazy_build`, a non-zero `build_max` or `build_overflow: "build"`:
`lx.wg.idle.* is set but this build lacks idle-suspend support; rebuild with -tags with_lx_idle_suspend (mobile-only feature)`.
`lx.wg.buffers` and `lx.wg.connected_socket` never take part in that refusal.
`sing-box check` validates the keys but does not start the router, so it accepts them on desktop.

The canonical form drops defaults written out explicitly: `read_datagram_count: 128`,
`send_by_mtu: true`, `connected_socket: false`, and an empty `idle` or `buffers` block.

Validation errors name the full key path:

- `lx.wg.idle.suspend_reachable must be >= lx.wg.idle.suspend`
- `lx.wg.idle.suspend_reachable requires lx.wg.idle.suspend`, and the same for `idle.teardown`, `idle.lazy_build`
- `lx.wg.idle.<key> must be >= 0` for a negative duration, `lx.wg.idle.build_max must be >= 0`
- `lx.wg.idle.build_overflow must be "wait" or "build"`
- `lx.wg.buffers.read_datagram_count must be between 1 and 128` for `0`, a negative value or above 128
- `lx.wg.<old key> is moved to lx.wg.idle.<new key>` for a flat key of the old layout; the key mapping is in [Removed keys](#removed-keys)

### `lx.masque` — MASQUE outbounds

| Key | Type | Default | Meaning |
|---|---|---|---|
| `idle_timeout` | duration ≥ 0 | off | Global idle window for every `masque` node without its own `idle_timeout`: after it the session (netstack, pumps, QUIC keepalive) is taken down and the next dial builds it again |

Priority: the node's `idle_timeout` > `lx.masque.idle_timeout` > off. An explicit `"0"` on a node
keeps that node's tunnel up even when the global value is set. MASQUE builds its session lazily
on its own, so there is no cap here.

Validation error: `lx.masque.idle_timeout must be >= 0` for a negative duration.

### `lx.trusttunnel` — TrustTunnel outbounds

| Key | Type | Default | Meaning |
|---|---|---|---|
| `idle_timeout` | duration ≥ 0 | `5m` | Global idle window for every `trusttunnel` node without its own `idle_timeout`: after it the session is closed locally and the next dial opens it again. Absent or `0` means 5 minutes |

Priority: the node's `idle_timeout` > `lx.trusttunnel.idle_timeout` > 5 minutes. An explicit `"0"`
on a node disables teardown of its session whatever the global value is. Unlike `lx.masque`, a
global `0` here does not turn the window off, it means "unset": teardown can only be disabled on
the node. The fields of the outbound itself, its `idle_timeout` included, are in
[protocols-transports.md §6.2](protocols-transports.md#62-outbound-fields).

Validation error: `lx.trusttunnel.idle_timeout must be >= 0` for a negative duration.

### `lx.mtu_align` — path MTU / packet-size alignment

A node can sit right above an IP tunnel: via `detour`, via a group under `detour`, or as a
`chain` link ([§10](#10-chain-outbound--a-virtual-multi-hop-path-of-groups-and-nodes)).
Such a node gets a packet size that fits the tunnel: `mtu` on tunnels (`wireguard`, `masque`),
`initial_packet_size` plus `disable_path_mtu_discovery: true` on QUIC proxies (`hysteria2`,
`tuic`, `hysteria`). The size is computed once at start from the config, before the nodes are
created, and the upstream constructors read the already-adjusted options. Values only go down;
every changed field is visible in the start log with its reason. The originating case is
issue #37: hysteria2 via `detour` over WARP worked against an IPv4 server and was silent against
an IPv6 one.

One key in the root `lx` block, two shapes:

```jsonc
"lx": { "mtu_align": "clamp" }                                        // string — the mode
"lx": { "mtu_align": { "mode": "clamp", "except": ["my-wg"] } }       // object — mode + exceptions
```

| Mode | Field not set | Set explicitly and above the limit | Set explicitly and fits |
|---|---|---|---|
| `off` | left alone | left alone | left alone |
| `fill` | limit is filled in | left alone; warning "does not fit under `<tag>`" | left alone |
| `clamp` (default) | limit is filled in | lowered to the limit; logged as configured → effective with the reason | left alone |

No mode ever raises a value. `except` lists node tags that are never touched in any mode. An
unknown mode is rejected when the config is parsed, an unknown tag later, when the instance is
built and every node is known. Without the key the mode is `clamp`. This is the `chain` contract
("`mtu` in the config = as standalone, the chain only lowers"), and it applies under a plain
`detour` as well.

Validation errors (`sing-box check` catches all three too):

- `lx.mtu_align.mode must be "off", "fill" or "clamp"` — an unknown mode, in the string and in
  the object form; at config parse
- `lx.mtu_align.except: empty tag` — an empty string in `except`
- `lx.mtu_align.except: unknown outbound/endpoint tag: <tags>` — the tag is neither an outbound
  nor an endpoint; checked before alignment, ahead of node creation

**Capacity of a node** — how many bytes of IP packet it carries for the node above it:

| Node below | Capacity |
|---|---|
| `masque`, `wireguard` (endpoint and legacy outbound), `openvpn-client`, `openconnect` | its effective `mtu` (after its own alignment, recursively downwards) |
| `tailscale` endpoint | `system_interface_mtu`, else 1280 |
| `selector`, `urltest` | min over all reachable members, recursively |
| `chain` as a `detour` target | unlimited + warning (a chain aligns itself) |
| any other node — `direct` and every proxy (vless, trojan, ss, socks, hysteria2, tuic, …), with or without `detour` | unlimited: a proxy delivers the packet of the node above whole (as a TCP stream, or by its own fragmentation), so whatever lies under its `detour` does not limit the node above |

Only tunnels and groups carry a capacity upwards. This rule leaves out one case: a tunnel over
`shadowsocks` or `socks` UDP whose own `detour` is a tunnel. Those pass datagrams through, and the
limit below them applies; set `mtu` on the upper tunnel by hand there.

**What the node above gets** (`ipudp` = 28 for an IPv4 server, 48 for IPv6; a domain or an
unparsed address counts as IPv6; a WG endpoint takes its family from `peers[0].address`):

| Node above | Field | Limit | Overhead v4 / v6 |
|---|---|---|---|
| `wireguard` | `mtu` | `capacity − 32 − s4 − ipudp` (`s4` = AmneziaWG transport padding, 0 for plain WG) | 60 / 80 (+ `s4`) |
| `masque` | `mtu` | `capacity − 51 − ipudp` | 79 / 99 |
| `hysteria2`, `tuic`, `hysteria` | `initial_packet_size` (+ `disable_path_mtu_discovery: true`) | `capacity − ipudp`, clamped 1200…1452 | — |
| `openvpn-client`, `openconnect` | — | not derived; one warning if there is capacity below the node | unknown |

**IPv6 floor.** A tunnel that carries IPv6 inside is never aligned below 1280. Such tunnels are a
`wireguard` endpoint with an IPv6 prefix in `address`, and `masque` always. gvisor silently refuses to bring
up IPv6 on a link with a smaller MTU, and a dead IPv6 inside the tunnel is worse than fragments. When
the path leaves less than 1280, the tunnel gets 1280 and a Warn line (`… is above the path: … leaves
1220 bytes, below the IPv6 minimum 1280; outer packets will be fragmented`). A tunnel with only IPv4
inside can be aligned below 1280; an explicit `mtu` below 1280 is never raised.

If a QUIC node's limit is below 1200 (a tunnel with `mtu` < 1248 under an IPv6 server), 1200 is set
with a warning: QUIC cannot go below its minimum. PMTUD is switched off together with the derived
`initial_packet_size`, because quic-go only knows how to search upwards to 1452: over IPv6 the
probes are lost with the fragments, over IPv4 they "pass" by fragmentation and every packet then
travels as two. A node with an explicit `initial_packet_size` is left alone entirely, PMTUD included.

**Example — default WARP** (`masque` with `mtu` 1280) and hysteria2 with a `detour` to it:

```jsonc
{ "type": "masque", "tag": "warp", "mtu": 1280, /* … */ },
{ "type": "hysteria2", "tag": "hy2", "server": "2001:db8::1", "detour": "warp", /* … */ }
```

`hy2` gets `initial_packet_size: 1232` (1280 − 48) and `disable_path_mtu_discovery: true`; with an
IPv4 server — 1252 (1280 − 28). Without alignment quic-go sends 1280-byte payloads, 1328 bytes over
IPv6. The tunnel splits them into fragments, and WARP drops those ([§4](#4-masque-outbound--cloudflare-warp)).

**Groups.** Under a group the min over all members is taken at start; switching the selector/urltest
does not recompute it. The QUIC packet size is fixed when the client is created, a WG device's MTU
when the device is created, and a live connection does not migrate to the new path. The cost on a
wide group member: up to 200 bytes per packet (1452 → 1252) and PMTUD off. If that cost matters,
there are three ways out: an explicit value in `fill` mode, the tag in `except`, or `off` mode.

**Start log** — one Info line per changed field, warnings at Warn:

```
mtu_align: hy2 initial_packet_size → 1232 (detour warp[masque] mtu 1280 − 48 ipv6; pmtud off)
mtu_align: wg-exit mtu 1420 → 1200 clamped (limited by warp[masque] mtu 1280 via sel −80 ipv6)
```

For `chain` the same decision shows in `ChainInfo` (`mtu_configured` / `mtu_effective` /
`mtu_reason` with the mode prefix `clamped:` / `filled:`). The lx RPC does not expose decisions
made for `detour`.

**Not aligned:**

- xhttp over h3 and naive (cronet) — they have their own QUIC transport;
- `openvpn-client` and `openconnect` as upper nodes — a warning only;
- the server → client direction — the server controls it.

What masque does from below (outer `InitialPacketSize` = `mtu + 51`, ICMP Packet Too Big with the
real budget) is in [§4](#4-masque-outbound--cloudflare-warp).

### `lx.sniff_guard` — guard against rerouting by SNI

A node between the client and the server can read the name out of the first handshake packet and
send the connection to that name instead of the dialled address. This is what an Xray inbound
with `sniffing.destOverride` and without `routeOnly` does. It shows up on a relay under `detour`
and on a router with a VPN or a transparent proxy in the local network. While the SNI is the
server's own name, the rerouting leads to the same place. When the SNI is a disguise (REALITY,
WARP, hysteria2 or tuic with a fake SNI, the AmneziaWG QUIC masquerade), the connection lands on
someone else's host and the node looks dead. The block makes the first handshake packet
unreadable to the sniffer and does not change what the server receives. From connection outcomes
the block learns which paths need this.

Without the block, as without a `method` in a section, there is no protection: the handshake goes
out as upstream sends it. A block in which neither section has a method is the same as no block.
The block does not turn itself on: protection defaults are up to the application that writes
the config.

```jsonc
"lx": {
  "sniff_guard": {
    "nodes": {
      "sni_mismatch": true,                 // absent = true; false — no filter
      "outbound_type": ["vless", "masque"]  // false | absent — no filter
    },
    "detour_type": {                        // no detour = "direct"
      "direct": "clean",
      "ssh":    "clean",
      "socks":  "clean",
      "*":      "suspect"
    },
    "override": false,
    "tls": {
      "method": "record_fragment",          // record_fragment | fragment | delay | off
      "delay": "300ms",
      "persist": true
    },
    "quic": {
      "method": "decoy_first_byte",         // decoy_first_byte | delay | off
      "detour_type": { "socks": "never" },
      "persist": true
    }
  }
}
```

**Which nodes — `nodes`.** Two filters; a node is guarded when it passes both. A node that does
not pass works as without the block. Without a `nodes` block `sni_mismatch: true` applies: a node
with its own name passes a sniffing relay unprotected anyway. `sni_mismatch: false` guards every
node that supports it.

| Key | Values | Absent | Meaning |
|---|---|---|---|
| `sni_mismatch` | `true`, `false` | `true` | `true` — only nodes whose SNI differs from the `server` address; `false` — no filter |
| `outbound_type` | a list of types, `false` | no filter | Only nodes of the listed types. `true`, an empty list and an unknown type are load errors; a known type that cannot be guarded is allowed and skipped |

Whether the SNI matches `server` is decided at load time by comparing strings, without DNS:
case-insensitive, trailing dot ignored, a subdomain is a different name. The name compared is the
one that actually goes into the ClientHello or Initial; with ECH, the outer, public one. The
outer name is taken only from an ECH config given inline in the node (`ech.config`); with
`ech.config_path` or ECH from DNS the plain `server_name` is compared.

| `server` | SNI | Result |
|---|---|---|
| domain | not set, or the same domain | match |
| domain | another name | mismatch |
| IP | a name | mismatch |
| IP | not set | match — nothing for a sniffer to read |
| any | no SNI sent (`disable_sni`) | match |
| hysteria2 with Salamander obfuscation | any | match — a sniffer cannot read the Initial |
| REALITY, masque (WARP), AWG QUIC masquerade | any | always mismatch |

A node behind a CDN (`server` is a CDN IP, SNI is its own domain) passes the filter without
needing to; the protection does not hurt it. A node whose name the relay's DNS resolves to a
different address does not pass `sni_mismatch`. Use `sni_mismatch: false` for it, narrowed with
`outbound_type` if needed.

**Starting state of a path — `detour_type`.** A map "carrier type → state the path starts in".
The carrier is the nearest non-group outbound the connection actually goes through: a group
(`selector`, `urltest`) resolves to its selected member. A node without `detour`, with `detour`
to `direct`, or to a group currently set to `direct`, is on a path of type `direct`. When the
carrier cannot be determined, the path is unknown: no protection, no label.

| Value | Meaning |
|---|---|
| `never` | Never any protection on this path: no label, no retries, node keys work as upstream |
| `clean` | Starts without protection |
| `suspect` | Starts with protection |

The `"*"` key covers every type not in the map. Groups cannot be keys. The map default: `direct`,
`ssh`, `socks` — `clean`; `"*"` — `suspect`. The `tls` or `quic` section can override single
types with its own `detour_type` map; the rest come from the shared one. `"socks": "never"` in
the `quic` section of the example comes from a field report: through `socks` with UDP-over-TCP
the decoy byte broke the tunnel even though there was no sniffer on the path.

**The `tls` and `quic` sections, `override`.**

| Key | Values | Absent | Meaning |
|---|---|---|---|
| `tls.method` | `record_fragment`, `fragment`, `delay`, `off` | `off` | Method for TLS over TCP (table below) |
| `quic.method` | `decoy_first_byte`, `delay`, `off` | `off` | Method for QUIC |
| `delay` (per section) | positive duration | `300ms` | Pause of that section's `delay` method. Against Xray it must exceed 200 ms; a smaller value is allowed for a sniffer that waits less. Zero and negative are load errors |
| `persist` (per section) | `true`, `false` | `false` | Keep labels of encrypted relays in `experimental.cache_file`. `true` without `cache_file` enabled is a load error |
| `detour_type` (per section) | a map as above | the shared map | Starting states overridden for that protocol |
| `override` (shared) | `true`, `false` | `false` | `false` — a node's `fragment` / `record_fragment` win over the block; a node with an explicit value follows it. `true` — for a node that passed `nodes`, the path label decides the ClientHello shape: the block's method on `suspect`, no split on `clean`, even if the node asked for a split. On a `never` path the block does nothing. QUIC has no node keys, `override` has no effect there |

The node keys `tls.fragment`, `tls.record_fragment` (an explicit `false` opts out of splitting)
and `tls.fragment_fallback_delay` work in any state of the block, as upstream.

**Methods.** The server receives a valid handshake in every case. Traffic after the handshake is
untouched.

| Method | What goes on the wire | Against the Xray sniffer | Cost |
|---|---|---|---|
| `tls.record_fragment` | The ClientHello split into several TLS records, split points inside the SNI name | The sniffer parses only the first record, which ends mid-extensions — "not a ClientHello" on the first try | None |
| `tls.fragment` | The ClientHello as one record, in chunks over TCP segments, waiting for the ACK (on the device's own socket) or pausing (through a relay) | Protects while the pause between chunks exceeds 200 ms: through a relay that is `fragment_fallback_delay` (500 ms), and on the direct path behind a transparent proxy the core holds the same pause | `fragment_fallback_delay` pause through a relay and on the direct path behind a transparent proxy |
| `tls.delay` | The ClientHello as one record, in two chunks split inside the SNI, with a `delay` pause between them | The first chunk is incomplete; the sniffer waits 200 ms for the rest and gives up | `delay` pause on every guarded handshake |
| `quic.decoy_first_byte` | A one-byte datagram `0x00` ahead of the first Initial; the server drops it | The QUIC sniffer decides on the first datagram: "not QUIC" | None |
| `quic.delay` | The second Initial is held back by `delay` | The sniffer assembles the ClientHello from Initial packets within 200 ms and runs out of time | `delay` pause on every guarded handshake |

**Which nodes can be guarded.** TLS over TCP: vless, trojan, vmess, anytls, shadowtls, http with
TLS, masque `h2`, trusttunnel `h2`, XHTTP on `h2`. QUIC: masque `h3`, hysteria2, tuic,
trusttunnel with QUIC, XHTTP on `h3`, wireguard with the QUIC-shaped AmneziaWG masquerade.
Skipped silently: naive and nodes with a system TLS engine (`engine: apple`, `windows`).

| Method | Applies to | Does not apply to |
|---|---|---|
| `tls.record_fragment`, `tls.fragment`, `tls.delay` | every TLS node listed above | naive, system TLS engines |
| `quic.decoy_first_byte` | every QUIC node listed, AmneziaWG included | — |
| `quic.delay` | QUIC nodes whose ClientHello spans two Initial packets | AmneziaWG; a node whose ClientHello fits in one Initial; hysteria2 with Salamander obfuscation. On such a node the method does nothing |

A node the section's method does not apply to goes as without the block and does not move the
path label.

**How the path label is learned.** A label is kept per "path + protocol": the path is `direct`
or the tag of the nearest relay, the protocol is `tls` or `quic`. The key is the tag, not the
type: two VLESS relays with different sniffing settings are told apart. The label is shared by
all nodes on that path; TLS and QUIC do not affect each other. On `clean` a node goes without
protection, on `suspect` with it.

The label changes only on a pair of outcomes on one node:

- the node failed without protection, then passed with it → the path becomes `suspect`;
- the node failed with protection, then passed without it → the path becomes `clean`;
- any other outcome leaves the label alone. A dead node that fails both ways changes nothing, so
  a bulk test of many nodes, some of them dead, does not take protection off a path.

A fast error (not a timeout) is retried in the same dial with the opposite decision. A timeout
returns the error, and the opposite decision goes to the node's next dial on that path (within
10 minutes). On a `suspect` path an attempt without protection after a failure is allowed at most
once per 10 minutes per path; the rest of the time a failure on `suspect` leads to nothing.

Only attempts in which the handshake actually happened and the block's decision was applied go
into a pair. A dial that lands on an already open session (mux, the anytls pool, XHTTP, a long
QUIC session of masque, hysteria2, tuic, trusttunnel) and a node with its own choice under
`override: false` do not.

Only nodes whose SNI differs from `server` (per the table above) move the label, regardless of
the `sni_mismatch` filter. A node with its own name passes a sniffing relay unprotected anyway, so
its outcomes say nothing about the sniffer. Such a node is protected by the label only with
`sni_mismatch: false`. On a `clean` path it does not retry. On a `suspect` path, if it failed
with protection and passed without it, the node records a personal opt-out: from then on it goes
unprotected on that path, while the path label and the other nodes stay as they were. The reverse pair on that node
(failed without, passed with) clears the opt-out. Retries for such a node happen at most once per
10 minutes. The opt-out lives as long as the core instance and is not persisted.

Protocol specifics:

- **hysteria2, tuic** — the handshake runs inside the QUIC library; the outcome is judged by the
  connection's lifetime: closed less than 30 s after the dial is a failure, longer is a success.
  A success counts as soon as the connection has lived 30 s; a failure is learnt when the node
  makes its next new connection. There is no retry within the same dial; a pair forms through the
  next dial. With hysteria2 port hopping,
  the sockets opened by later hops carry no handshake and are not guarded; hysteria2 realm mode
  is not guarded at all.
- **masque `auto`** — the legs follow different labels: h3 the `quic` one, h2 the `tls` one. A
  fast-failing h3 is retried with the opposite decision before the h2 fallback. A silent h3 yields
  to h2 after 3 s without a handshake, and the opposite decision goes to the next h3 leg.
- **AmneziaWG** — a `wireguard` endpoint is covered when its masquerade is `ip: "quic"` or it has
  a non-empty `i1`; the byte goes only ahead of a QUIC-shaped burst. The decision is taken from
  the label when the socket is created. The handshake outcome does not move the label, so
  AmneziaWG never moves a path label. With no other QUIC node on the path the label stays at its
  start. For an AWG node on a path that starts `clean` (direct, `socks`) set protection
  explicitly: `suspect` for that carrier type in `quic.detour_type`.

Every label change and every personal opt-out set or cleared writes an info-level log line: path,
protocol, old and new state, the node, and the outcomes of both attempts. There is no separate
API.

**Cost of learning.**

- The first failure on a `clean` path is a timeout, not an error: the sniffer reroutes the
  connection to a name where nobody answers. The node's first dial hangs until its deadline, its
  first URL test fails, protection arrives from the second dial.
- A node that fails fast costs two handshakes instead of one.
- A random glitch on a clean path (a failure without protection followed by a success with it)
  sets `suspect`, and only the reverse pair clears it. For `record_fragment` and
  `decoy_first_byte` this costs nothing; for `delay` and `fragment` it is a pause on every
  handshake, with `persist` across restarts too.

**Network change and `persist`.** A network change resets the labels of the `direct` path and of
unencrypted carriers (`socks`, `http` without TLS) to their start, together with the personal
opt-outs and pending retries on them. Labels of encrypted relays survive a network change. If a
different relay (type, address or port) shows up under the same tag, its labels and opt-outs are
reset. Without `persist` labels live as long as the core instance: a restart or applying a new
config starts from the starting states. With `persist` the labels of encrypted relays are written
to `experimental.cache_file` and loaded at start if the same relay is under the same tag. An
entry for a path that the current config's map declares `never` is not applied. Labels of
`direct` and unencrypted carriers are not stored.

**Load errors.** All of them are caught by `sing-box check`. `<key>` is the full path of the key
in question, for example `lx.sniff_guard.quic.detour_type`.

| Cause | Message |
|---|---|
| Unknown method | `lx.sniff_guard.tls.method must be one of "record_fragment", "fragment", "delay", "off"`; `lx.sniff_guard.quic.method must be one of "decoy_first_byte", "delay", "off"` |
| `nodes.outbound_type: true` | `lx.sniff_guard.nodes.outbound_type: true is not a filter; list the types, or use false for no filter` |
| `nodes.outbound_type: []` | `lx.sniff_guard.nodes.outbound_type: empty list; list the types, or use false for no filter` |
| A type no outbound or endpoint of this build knows, in `nodes.outbound_type` or as a `detour_type` key | `<key>: unknown outbound type "<t>"` |
| A group in `nodes.outbound_type` | `lx.sniff_guard.nodes.outbound_type: "<t>" is a group; a group is never dialled itself, name the member types` |
| A group as a `detour_type` key | `<key>: "<t>" is a group; name the member types, a group is resolved to the member it picks` |
| An unknown value in a `detour_type` map | `<key>.<type> must be "never", "clean" or "suspect"` |
| A zero or negative `delay` | `<key>.delay must be > 0` (`<key>` is `lx.sniff_guard.tls` or `lx.sniff_guard.quic`) |
| `persist: true` without `experimental.cache_file` enabled | `lx.sniff_guard.tls.persist: requires experimental.cache_file to be enabled`; the same with `.quic` |
| An unknown key, or a value that is not an object | the message starts with the key: `lx.sniff_guard`, `lx.sniff_guard.nodes`, `lx.sniff_guard.tls`, `lx.sniff_guard.quic` |
| The removed `lx.quic_sniff_decoy` key | `lx.quic_sniff_decoy is removed: the QUIC sniff decoy is configured by lx.sniff_guard.quic` |

The unknown-type and `persist` checks run only when at least one section has a method; `persist`
is checked for the section that has one.

**Limits.**

- A sniffer is not detected directly and not told apart from other causes of failure. The only
  sign is a pair of outcomes on one node, and a random glitch can form a false pair.
- A handshake that completes without an error in the wrong place counts as a success: `tuic` with
  `insecure` and an SNI that resolves to a live QUIC host. masque is protected by key pinning,
  hysteria2 by authentication, TLS nodes by certificate verification (without `insecure`).
- The full gRPC transport (`grpc`, not lite) is not shaped: its handshake runs outside the node's
  dial. Such a node goes as without the block.
- On the direct path shadowtls v2/v3 and any node with `tcp_fast_open` are not protected: the
  path cannot be classified. Through `detour` they are protected like the others.
- shadowtls v3 without uTLS is not protected on any path: its handshake bypasses the core TLS
  layer. With uTLS it is protected like the others.
- The block does not fix the relay; the fix on the relay side is `"routeOnly": true` in
  `sniffing`.
- Not tested against sniffers other than Xray. sing-box as a relay does not reroute by sniffing
  and needs no protection. The 200 ms wait is an Xray constant; for another sniffer tune `delay`.
- A DPI that drops a guarded handshake (treating record splitting as a signature) is not bypassed:
  the pair moves that path's label to `clean`.

### Removed keys

A config with any of these keys fails to load.

| Key | Instead |
|---|---|
| `lx.quic_sniff_decoy` | `lx.sniff_guard.quic` with `"method": "decoy_first_byte"` |
| `lx.awg_masquerade` | nothing: the AmneziaWG burst goes on every path |
| `lx.wg.idle_suspend`, `idle_suspend_reachable`, `idle_teardown`, `lazy_build`, `build_max`, `build_overflow` | the same key in `lx.wg.idle`: `idle_suspend` → `idle.suspend`, `lazy_build` → `idle.lazy_build` |
| `route.lx_idle_suspend`, `lx_idle_suspend_reachable`, `lx_idle_teardown` | `lx.wg.idle.suspend`, `suspend_reachable`, `teardown` |
| `network` in a `masque` node | `vhttp` |

### Reserved: `lx.naive`

`single_engine` and `lazy_start` are reserved for SPEC 096. Until that lands, an `lx.naive`
sub-block is rejected like any unknown key.

## 14. Tailscale: turning Taildrop off

The `disable_taildrop` key on a `tailscale` endpoint. Without it Taildrop works as upstream.

```json
{ "type": "tailscale", "tag": "ts", "disable_taildrop": true }
```

With `true` the node accepts no files: the sender gets a 403 `Taildrop is disabled on this node`. Sending from the node is refused as well, and the status reports `canShareFiles = false`. Files received earlier stay in `taildrop_directory` and can still be opened and deleted. Other devices on the tailnet still see the node as a target: that is set by the Tailscale admin console, not by the node.

