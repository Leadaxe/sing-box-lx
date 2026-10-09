# Protocols & transports — full parameter reference

> 🌐 Русская версия: **[protocols-transports.ru.md](protocols-transports.ru.md)**.

> 🧭 **Where to look.** The fork's documentation has three levels, by the reader's question:
> [lx-config](lx-config.md) — what the fork has and how to enable it;
> [protocols-transports](protocols-transports.md) — every field, type, default, error text;
> [xray-protocols-explained](xray-protocols-explained.md) and [amneziawg-explained](amneziawg-explained.md) —
> how it works, why, how the fork does it and how it differs from vanilla.

Exhaustive, field-by-field reference for the downstream protocol/transport
features of `sing-box-lx`:

| Feature | Build tag | Where it attaches | Chapter |
|---------|-----------|-------------------|---------|
| **XHTTP** transport (Xray "splithttp"/"xhttp") | `with_xhttp` | `transport` block of a VLESS / VMess / Trojan **outbound** | [§1](#1-xhttp-transport) |
| **AmneziaWG 2.0/3.x** (AWG2, AWG3) obfuscation | `with_awg` | promoted fields on a `wireguard` **endpoint** | [§2](#2-amneziawg-203x-awg2-awg3) |
| **MASQUE** outbound (CONNECT-IP / WARP) | `with_quic` + `with_gvisor` | `outbounds[].type: "masque"` | [§3](#3-masque-outbound-connect-ip--warp) |
| **REALITY** client (hybrid key share, `key_share`, fragmentation) and **VLESS `encryption`** | — (REALITY inside `with_utls`) | `tls` block of a TLS-over-TCP **outbound**; flat `encryption` field on `vless` | [§5](#5-reality-and-vless-encryption) |

Plus [§4](#4-grpc-transport) — the **gRPC** transport, which is upstream's, not
ours, and appears here only for the one place our behaviour has downstream
interop notes: the forms of `service_name`.

For a high-level tour of every downstream feature (idle-suspend, DNS group, VLESS
`encryption`, `lxd`, observability), and short "getting started" examples, see
**[lx-config.md](lx-config.md)**. This document is the deep reference the config
overview links to: every field, its type, its default, its validation, and the
exact error text you get when it is wrong.

Everything here is sourced from the option structs (`option/v2ray_xhttp.go`,
`option/wireguard_awg.go`, `option/masque_lx.go`) and the protocol implementations,
not from memory — defaults and error strings are the ones the current core emits.

> ⚠️ Every key / UUID / address below is a **placeholder**. Never commit real
> private keys or pre-shared keys to a repository.

**Build once, use everywhere.** The desktop/CLI binary bundles all three:

```bash
make -f Makefile.lx lx-build
```

Without the tag the feature is absent and a config that uses it is **rejected at
load time** with an explicit error — never a silent downgrade to a plain
transport (which would defeat the obfuscation). The exact messages:

- XHTTP without `with_xhttp` → `XHTTP transport requires the with_xhttp build tag`
- AWG field without `with_awg` → `AmneziaWG (awg) support is not included in this build, rebuild with -tags with_awg`
- `masque` outbound without `with_quic`+`with_gvisor` → the `masque` type is not registered (`unknown outbound type: masque`)

---

## Table of contents

- [§1 XHTTP transport](#1-xhttp-transport)
  - [1.1 Modes](#11-modes)
  - [1.2 Core fields (v1)](#12-core-fields-v1)
  - [1.3 Session / seq placement (v2)](#13-session--seq-placement-v2)
  - [1.4 Uplink-data placement (v2, packet-up)](#14-uplink-data-placement-v2-packet-up)
  - [1.5 X-Padding obfuscation (v2)](#15-x-padding-obfuscation-v2)
  - [1.6 Packet-up tuning (v2)](#16-packet-up-tuning-v2)
  - [1.7 Connection reuse — `xmux`](#17-connection-reuse--xmux)
  - [1.8 Accepted-but-ignored fields](#18-accepted-but-ignored-fields)
  - [1.9 Range value forms](#19-range-value-forms)
  - [1.10 Examples](#110-examples)
  - [1.11 Troubleshooting](#111-troubleshooting)
- [§2 AmneziaWG 2.0/3.x (AWG2, AWG3)](#2-amneziawg-203x-awg2-awg3)
  - [2.1 The model: AWG1 vs AWG2 vs AWG3](#21-the-model-awg1-vs-awg2-vs-awg3)
  - [2.2 Junk & signature fields](#22-junk--signature-fields)
  - [2.3 Magic headers `h1`–`h4`](#23-magic-headers-h1h4)
  - [2.4 CPS decoys `i1`–`i5` and the tag format](#24-cps-decoys-i1i5-and-the-tag-format)
  - [2.5 Masquerade sugar `id` / `ip` / `ib`](#25-masquerade-sugar-id--ip--ib)
  - [2.6 MTU budget](#26-mtu-budget)
  - [2.7 Mapping an `awg.conf` 1:1](#27-mapping-an-awgconf-11)
  - [2.8 Examples](#28-examples)
  - [2.9 Validation errors (verbatim)](#29-validation-errors-verbatim)
  - [2.10 AWG 3.x: header protection, padding, trailers, timings](#210-awg-3x-header-protection-padding-trailers-timings)
- [§3 MASQUE outbound (CONNECT-IP / WARP)](#3-masque-outbound-connect-ip--warp)
  - [3.1 What it is](#31-what-it-is)
  - [3.2 MASQUE-specific fields](#32-masque-specific-fields)
  - [3.3 Inherited DialerOptions](#33-inherited-dialeroptions)
  - [3.4 Profiles — what `profile` switches](#34-profiles--what-profile-switches)
  - [3.5 Key material & value formats](#35-key-material--value-formats)
  - [3.6 SNI strategy](#36-sni-strategy)
  - [3.7 `vhttp`: auto / h3 / h2](#37-vhttp-auto--h3--h2)
  - [3.8 Idle-suspend & keepalive](#38-idle-suspend--keepalive)
  - [3.9 Start-time validation (fail-fast)](#39-start-time-validation-fail-fast)
  - [3.10 Migrating from the pre-SPEC-062 shape](#310-migrating-from-the-pre-spec-062-shape)
  - [3.11 Examples](#311-examples)
  - [3.12 Common footguns](#312-common-footguns)
- [§4 gRPC transport](#4-grpc-transport)
  - [4.1 `service_name`: the Xray forms](#41-service_name-the-xray-forms)
- [§5 REALITY and VLESS `encryption`](#5-reality-and-vless-encryption)
  - [5.1 `tls.reality` fields](#51-tlsreality-fields)
  - [5.2 Fingerprints and the hybrid key share](#52-fingerprints-and-the-hybrid-key-share)
  - [5.3 ClientHello fragmentation](#53-clienthello-fragmentation)
  - [5.4 REALITY validation errors (verbatim)](#54-reality-validation-errors-verbatim)
  - [5.5 The `encryption` field on `vless`](#55-the-encryption-field-on-vless)
  - [5.6 `encryption` validation errors (verbatim)](#56-encryption-validation-errors-verbatim)
  - [5.7 Examples](#57-examples)

---

# 1. XHTTP transport

> 🧭 How XHTTP works, why, and how the fork implements it: [xray-protocols-explained §4](xray-protocols-explained.md#4-xhttp); symptom-based troubleshooting is there too, [§4.6](xray-protocols-explained.md#46-pitfalls-where-the-connection-stays-silent). This chapter holds only fields, defaults and errors.

XHTTP (Xray "splithttp"/"xhttp") is a v2ray transport that tunnels the proxy over
plain HTTP requests — HTTP/2 by default, HTTP/1.1 or HTTP/3 when the TLS block
asks for them ([HTTP version](#http-version)). It attaches to **VLESS / VMess / Trojan** through the shared
`transport` block and composes with TLS, including **Reality**. (XTLS-Vision
runs over XHTTP only when the VLESS outbound also sets `encryption`: Vision then
sits on the encryption layer, not on the transport's TLS — same as Xray.)

JSON keys are snake_case to match Xray's stream settings, sing-box-extended and the
rest of sing-box. **The default wire shape (everything below at its default) is
byte-identical to the live-verified v1 client**, so existing configs are
unaffected — every v2 field is opt-in.

## 1.1 Modes

`mode` selects how the request/response streams are shaped (Xray-compatible):

| Mode | Shape |
|------|-------|
| `auto` (default) | resolves to `stream-one` on a Reality TLS, otherwise `packet-up` — the same rule Xray applies |
| `packet-up` | a separate GET download stream + sequential POST upload packets |
| `stream-up` | a single streamed POST upload + a separate GET download stream |
| `stream-one` | a single bidirectional HTTP stream (request body up, response body down) — the closest analogue to httpupgrade |

Leave `mode` at `auto` unless the server documents otherwise; set it explicitly
only if you know what the server expects.

### HTTP version

There is no separate key: the HTTP version follows from the outbound's `tls`
block, by the same rule as Xray (`decideHTTPVersion`, SPEC 104). Every mode
works on every version.

| TLS | Reality | `tls.alpn` | Version | Wire |
|-----|---------|-----------|---------|------|
| off | — | — | HTTP/1.1 (h2c before SPEC 104) | TCP |
| on | on | any | HTTP/2 | TCP + Reality |
| on | off | empty | HTTP/2 | TCP + TLS, ALPN `h2` |
| on | off | two or more entries | HTTP/2 | TCP + TLS, ALPN as configured |
| on | off | `["http/1.1"]` | HTTP/1.1 | TCP + TLS |
| on | off | `["h3"]` | HTTP/3 | UDP + QUIC |
| on | off | any other single entry | HTTP/2 | TCP + TLS, ALPN as configured |

- The entry is compared exactly (`h3`, `http/1.1`).
- **HTTP/3** needs a build with `with_quic` (all shipped builds have it); without
  it a config with `alpn: ["h3"]` fails to load. The QUIC handshake uses the
  Chrome profile; `utls.fingerprint` does not apply over HTTP/3 (a load-time
  warning, the config still loads — Xray behaves the same). With `disable_sni`
  the Chrome profile is turned off for that server (warning). ECH is not
  supported over HTTP/3: the config loads, dials of that server fail.
  HTTP/3 traffic goes over UDP through the same `detour`.
- **HTTP/1.1**: long requests (the download GET, a streamed POST) each get their
  own connection with `Connection: close`; `packet-up` upload POSTs reuse
  keep-alive connections.
- **Reality is always HTTP/2.** A `tls.alpn` without `h2` is replaced with
  `["h2"]` (warning), otherwise a Reality server with `alpn: ["h3"]` would not
  come up over TCP.
- **Without TLS the client uses HTTP/1.1, as Xray does (it used h2c before).**
  An Xray server accepts both forms, so direct cleartext nodes keep working, and
  nodes behind a reverse proxy or CDN that only speaks HTTP/1.1 on a cleartext
  port start working. There is no multiplexing: every download stream and every
  streamed request takes its own TCP connection.
- Not supported: Xray `downloadSettings`, `finalmask.quicParams` (QUIC runs
  with Xray's defaults, congestion control is Cubic where Xray uses BBR).

## 1.2 Core fields (v1)

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `type` | string | — | selector — must be `"xhttp"` |
| `mode` | string | `auto` | see [§1.1](#11-modes) |
| `host` | string | TLS SNI / server address | overrides the HTTP `Host` header |
| `path` | string | `""` (root) | request path prefix; the session id (and, for `packet-up`, the upload sequence number) are appended as path segments when their placement is `path` |
| `headers` | object | — | extra request headers sent on every XHTTP request |
| `x_padding_bytes` | string range | `"100-1000"` | inclusive byte-length **range** of the padding value (`"min-max"` or a single int). Drives both the legacy Referer `x_padding` length and the obfs-mode padding length |
| `no_grpc_header` | bool | `false` | omit the `Content-Type: application/grpc` header that streamed-body modes (`stream-one`, `stream-up`) carry by default, mirroring Xray's `NoGRPCHeader`. That header is load-bearing in front of reverse proxies / CDNs, which key response streaming (no buffering) on a gRPC content type — without it a `stream-one` dial can hang until timeout. Leave off unless the server rejects the gRPC content type |

## 1.3 Session / seq placement (v2)

Where the per-request **session id** and (packet-up) **upload sequence number** are
carried. The session id identifies the logical connection across the split
upload/download streams; the seq orders the upload POSTs.

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `session_placement` | string | `path` | `path` \| `query` \| `header` \| `cookie` |
| `session_key` | string | `X-Session` (header) / `x_session` (query\|cookie) | name carrying the session id when placement ≠ `path`; unused for `path` |
| `seq_placement` | string | `path` | `path` \| `query` \| `header` \| `cookie`. For `path`, the seq is the **second** appended segment |
| `seq_key` | string | `X-Seq` (header) / `x_seq` (query\|cookie) | name carrying the seq when placement ≠ `path`; unused for `path` |
| `session_table` | string | unset | alphabet the random session id is drawn from: a predefined name or a literal ASCII set. Client-only |
| `session_length` | string | unset | session id length, `"min-max"` or `"n"`. Only used together with `session_table` |

> **Session id shape (`session_table` / `session_length`).** By default the session
> id is a dashed UUID (`8f14e45f-ceea-4d31-9d4f-d0b8e5c1a2b7`), which is what an
> unconfigured Xray peer also sends. These two options replace it with a random
> string of your choosing — e.g. `"session_table": "Base62"` with
> `"session_length": "16-32"` yields `k7Qm2XpR9vLdA3wZ`. The point is to drop the
> recognizable UUID fingerprint from the URL, so pick a shape that matches the
> service you are imitating.
>
> Predefined alphabet names (**case-sensitive**, byte-for-byte Xray's set):
> `hex`, `HEX`, `number`, `alphabet`, `Alphabet`, `ALPHABET`, `base36`, `BASE36`,
> `Base62`. Anything else is taken as a literal ASCII alphabet.
>
> Both options must be set together — setting one alone is rejected rather than
> silently falling back to UUIDs. The length floor must be above 0, and the id
> space (`len(table) ^ min`) must exceed 2^31, so two independent clients do not
> draw the same id and get merged into one server-side session.
>
> **The server is never told.** It treats the session id as an opaque grouping key,
> so this is a client-only knob: no server change is needed, and an Xray server
> accepts either form. Xray calls these `sessionIDTable` / `sessionIDLength`.

> **Path segment order is load-bearing.** With both on `path` (the default), the
> session id is appended **first** and the seq **second** — the server splits on
> that order. Do not reorder them by hand.

## 1.4 Uplink-data placement (v2, packet-up)

Where the upload payload goes. `header`/`cookie` are **only valid in `packet-up`
mode** — using them in another mode is a load-time error.

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `uplink_data_placement` | string | `auto` | `body` \| `auto` (== body) \| `header` \| `cookie`. `header`/`cookie` carry the payload as `base64.RawURLEncoding`, chunked into `<key>-<i>` headers / `<key>_<i>` cookies |
| `uplink_data_key` | string | `X-Data` (header/auto) / `x_data` (cookie) | base name for the chunked header/cookie payload; `""` for body |
| `uplink_chunk_size` | string range | cookie `2048-3072`, header `3000-4000`, else `= sc_max_each_post_bytes` | `"min-max"` range (in base64 chars) of each chunk; the min is floored to 64 |
| `uplink_http_method` | string | `POST` | HTTP method for **upload** requests (download is always GET); upper-cased. `GET` is allowed **only** in `packet-up` |

## 1.5 X-Padding obfuscation (v2)

Only active when `x_padding_obfs_mode` is `true`; otherwise the legacy Referer
padding (see note in [§1.10](#110-examples)) is used. The padding **length** is
always driven by `x_padding_bytes` ([§1.2](#12-core-fields-v1)); the fields here
choose *where* and *how* the padding bytes are placed.

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `x_padding_obfs_mode` | bool | `false` | master switch. `false` → legacy Referer `x_padding`. `true` → the configurable `x_padding_*` family below |
| `x_padding_placement` | string | `queryInHeader` | `cookie` \| `header` \| `query` \| `queryInHeader` |
| `x_padding_key` | string | `x_padding` | cookie/query param name (unused for `header` placement) |
| `x_padding_header` | string | `X-Padding` | header name (for `header` / `queryInHeader` placement) |
| `x_padding_method` | string | `repeat-x` | `repeat-x` (N literal `X` bytes) \| `tokenish` (base62 token whose HPACK-Huffman length is tuned to ~N) |

## 1.6 Packet-up tuning (v2)

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `sc_max_each_post_bytes` | string range | `"1000000-1000000"` | `"min-max"` range bounding a single upload POST (the split threshold) |
| `sc_min_posts_interval_ms` | string range | `"30-30"` | `"min-max"` anti-burst delay between successive POSTs, in ms |

## 1.7 Connection reuse — `xmux`

Without a pool every XHTTP stream pays a full TCP + TLS (+ REALITY) handshake.
`xmux` reuses HTTP connections, and — just as important — it is what Xray servers
expect: an `xmux` section arriving from a subscription used to be ignored silently,
so the client behaved differently from what the server author intended.

**A `nil`/absent `xmux` section still enables XMUX with Xray-compatible defaults** —
the pool is always on, matching Xray-core. The defaults apply only when the section
is absent or entirely empty; a section with any field set takes every field as written.

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `xmux.max_concurrency` | range | unlimited | how many streams may share one HTTP connection. **Mutually exclusive** with `max_connections` |
| `xmux.max_connections` | range | `3-3` | how many connections the pool holds; below this count a new connection is always opened. **Mutually exclusive** with `max_concurrency` |
| `xmux.c_max_reuse_times` | range | unlimited | how many times a connection may be handed out for a new stream before it retires |
| `xmux.h_max_request_times` | range | `600-900` | how many **HTTP requests** may traverse a connection before it retires. Counts requests, not streams — in `packet-up` one stream issues many upload POSTs |
| `xmux.h_max_reusable_secs` | range | `1800-3000` | how long a connection stays reusable, in seconds |
| `xmux.h_keep_alive_period` | int (seconds) | `0` = default | Keep-alive period. HTTP/2: ping period (the transport's `ReadIdleTimeout`), `0` = no pings. HTTP/3: QUIC keep-alive, `0` = 10 s. No effect on HTTP/1.1. Negative disables keep-alive. A plain integer, **not** a range — matching the reference implementation |

**Each range is rolled once, not per request:** the manager rolls `max_concurrency`
and `max_connections` at construction; every connection rolls its own reuse limits
when it is created. A connection at its limit is retired but **not torn down while
streams still use it** — closing is deferred until the last one finishes.
Client-only: the server half and the `download` section are out of scope.

## 1.8 Accepted-but-ignored fields

Present so an inbound-shaped config or a symmetric link doesn't error — the client
never acts on them:

| Key | Why it exists |
|-----|---------------|
| `sc_max_concurrent_posts` | legacy Xray knob (removed from current Xray-core); current Xray serializes to one POST body in flight, matching our sequential upload |
| `server_max_header_bytes` | server-only (`http.Server.MaxHeaderBytes`) |
| `no_sse_header` | server-only (omit `Content-Type: text/event-stream` on the stream-down response); the client never inspects `Content-Type` |
| `sc_max_buffered_posts` | server-only (upload reorder buffer depth) |
| `sc_stream_up_server_secs` | server-only ("min-max" keepalive-padding interval on the stream-up response). The client does **not** strip server-injected keepalive padding from the stream-up download — verify against the target server if it emits any |

## 1.9 Range value forms

Every field marked "range" (`x_padding_bytes`, `uplink_chunk_size`, `sc_*`, all
`xmux.*` except `h_keep_alive_period`) accepts three forms:

- `"min-max"` string — `"600-900"`
- a single integer — `4` (equivalent to `4-4`) or `"4000"`
- a two-element JSON array — `[600, 900]` (for configs authored against Xray /
  sing-box-extended)

An empty value selects the documented default.

## 1.10 Examples

### VLESS + XHTTP + Reality (`stream-one`)

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

### packet-up behind a CDN, with an xmux pool

```jsonc
{
  "type": "vless",
  "tag": "xhttp-cdn",
  "server": "cdn.example.com",
  "server_port": 443,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "tls": { "enabled": true, "server_name": "cdn.example.com" },
  "transport": {
    "type": "xhttp",
    "mode": "packet-up",
    "path": "/down",
    "sc_max_each_post_bytes": "800000-1000000",
    "sc_min_posts_interval_ms": "10-30",
    "xmux": {
      "max_concurrency": "4-8",
      "h_max_request_times": "600-900",
      "h_max_reusable_secs": "1800-3000",
      "h_keep_alive_period": 45
    }
  }
}
```

### HTTP/3 (`alpn: ["h3"]`)

The server listens for XHTTP over QUIC only (Xray `tlsSettings.alpn: ["h3"]`).

```jsonc
{
  "type": "vless",
  "tag": "xhttp-h3",
  "server": "example.com",
  "server_port": 443,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "tls": {
    "enabled": true,
    "server_name": "example.com",
    "alpn": ["h3"],
    "certificate_public_key_sha256": ["<base64-sha256-of-the-server-key>"]
  },
  "transport": {
    "type": "xhttp",
    "mode": "stream-up",
    "path": "/xhttp"
  }
}
```

> **Note (default wire format).** With `x_padding_obfs_mode` off (the default),
> padding is carried as `x_padding=<zeros>` inside the `Referer` header (Xray's
> default placement) — live-validated against a real Xray (3x-ui) server. The
> server validates the `x_padding` length (default 100–1000) and replies `400`
> without it. Client and server Xray versions should still match (XHTTP evolves
> quickly).

## 1.11 Troubleshooting

Symptom-based troubleshooting lives in the explanatory document:
[xray-protocols-explained §4.6 "Pitfalls where the connection stays silent"](xray-protocols-explained.md#46-pitfalls-where-the-connection-stays-silent).
This reference keeps only load-time error texts: [§1.8](#18-accepted-but-ignored-fields) and
the field tables above.

---

# 2. AmneziaWG 2.0/3.x (AWG2, AWG3)

> 🧭 How AmneziaWG works, the MTU budget, the graft and validation: [amneziawg-explained](amneziawg-explained.md); typical failures are there too, [§10](amneziawg-explained.md#10-typical-failures). This chapter holds only fields, defaults and errors.

AWG is WireGuard + DPI-evasion obfuscation: AWG2 reshapes the packets, AWG3 additionally
encrypts their headers and randomises sizes and timings ([§2.10](#210-awg-3x-header-protection-padding-trailers-timings)). It is configured as a normal sing-box
**`wireguard` endpoint** with extra promoted fields (all at the endpoint **root**,
none on a peer — mirroring an `awg-quick` `.conf` `[Interface]` section). With
`with_awg` these are pushed to the device; a config without any AWG field is a plain
WireGuard endpoint, **byte-identical to upstream behavior**.

The runtime is backed by `Leadaxe/wireguard-go` (sagernet/wireguard-go + AmneziaWG
obfuscation, wired via the `submodules/wireguard-go` submodule).

## 2.1 The model: AWG1 vs AWG2 vs AWG3

| Layer | Keys |
|-------|------|
| **AWG1** | `jc`, `jmin`, `jmax`, `s1`, `s2`, `h1`–`h4` as single values |
| **AWG2** | AWG1 plus `i1`–`i5`, `s3`/`s4`, `h1`–`h4` as `"min-max"` ranges, the `id`/`ip`/`ib` sugar |
| **AWG3** | AWG2 plus `header_protection_key`, `content_padding_addition`, `random_trailers`, `disable_cookies`, ranged timings and a ranged `persistent_keepalive_interval` ([§2.10](#210-awg-3x-header-protection-padding-trailers-timings)) |

What each layer does to the packets and why: [amneziawg-explained §0–§4](amneziawg-explained.md#0-the-whole-picture-three-layers-on-top-of-wireguard).

Both client and server must run AmneziaWG with **matching** parameters — the junk
and I-packets are *configuration*, not negotiated. Set them from the same
`awg.conf` on both ends.

## 2.2 Junk & signature fields

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `jc` | int | `0` (unset) | number of junk packets sent before the handshake |
| `jmin` | int | `0` | minimum size of those junk packets |
| `jmax` | int | `0` | maximum size of those junk packets. Keep it **below** the real path MTU (see [§2.6](#26-mtu-budget)) |
| `s1` | int | `0` | junk prepended to the handshake **INIT** message |
| `s2` | int | `0` | junk prepended to the handshake **RESPONSE** message |
| `s3` | int | `0` | AWG 2.0 junk-size param; pads only **cookie-reply** messages (no MTU impact) |
| `s4` | int | `0` | AWG 2.0 junk-size param; prepended to **every transport (data) message** — this is what drives the lower [MTU](#26-mtu-budget) requirement |

## 2.3 Magic headers `h1`–`h4`

`h1`–`h4` override WireGuard's four message-type magic values. Each is either a
single `uint32` **or** an inclusive `"min-max"` range string.

| Key | Type | Meaning |
|-----|------|---------|
| `h1` | int \| `"min-max"` string | magic for message type 1 (default `1`) |
| `h2` | int \| `"min-max"` string | magic for message type 2 (default `2`) |
| `h3` | int \| `"min-max"` string | magic for message type 3 (default `3`) |
| `h4` | int \| `"min-max"` string | magic for message type 4 (default `4`) |

- A **single** `uint32` (`1234567890`, AWG 1.x style) fixes the value.
- A **range** string (`"43613244-384550127"`, AWG 2.0 ranged headers) makes the
  device pick a random value from the range per message.
- `0` **or** `""` = unset (counts as the WireGuard default `1`/`2`/`3`/`4`). A plain
  number `N` is equivalent to the range `"N-N"`.
- On the wire, a single value marshals back to a JSON **number**; only a range
  becomes a JSON **string** (type fidelity with the old `uint32` field).

> **Ranged headers must not overlap.** The four ranges (an unset header counting as
> its WireGuard default) **must not overlap**, or the device rejects the config with
> `headers must not overlap`. Set all four together, as AWG2 exports do.

## 2.4 CPS decoys `i1`–`i5` and the tag format

`i1`–`i5` are AWG 2.0 **Controlled Packet Sequence (CPS)** decoy packets, sent in
order *before* the handshake. They are **case-sensitive** tag-format strings and map
1:1 to the amneziawg-go `i1`..`i5` keys.

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `i1` … `i5` | string | `""` | CPS decoy packets, sent in order. `i1` typically mimics a real protocol (e.g. a QUIC/STUN header) and is **mutually exclusive with the [`id`/`ip`/`ib`](#25-masquerade-sugar-id--ip--ib) sugar** |

**Tag format** (UPPERCASE keywords, order matters):

| Tag | Emits |
|-----|-------|
| `<b 0xHEX>` | static bytes (the literal hex) |
| `<c>` | a counter |
| `<t>` | a timestamp |
| `<r N>` | N random bytes |
| `<rc N>` | N random characters |
| `<rd N>` | N random digits |

Example: `"i1": "<b 0x000100002112a442><r 12>"` — a static 8-byte prefix followed by
12 random bytes.

## 2.5 Masquerade sugar `id` / `ip` / `ib`

Hand-writing an `i1` CPS string is fiddly. As a friendlier alternative — the same
naming [WireSock Secure Connect](https://www.wiresock.net/) uses — you declare a
masquerade by **domain / protocol / browser** and the device generates the `i1`
decoy for you.

| Key | Type | Meaning |
|-----|------|---------|
| `id` | string | masquerade **domain** (a host that looks normal for your region, e.g. `www.google.com`). Strict LDH hostname. Embedded into the decoy for `ip=quic` (as the ClientHello **SNI**), `ip=dns` (as the **QNAME**) and `ip=sip` (as the **host**); `ip=stun` has nowhere to carry a hostname and ignores it. **Required only for `quic`**; for `dns`/`sip` a pseudo name is generated when absent; `stun` ignores it. Whenever set, it is LDH-validated (injection-y values are rejected) |
| `ip` | string | masquerade **protocol**: `quic` \| `dns` \| `stun` \| `sip` |
| `ib` | string | masquerade **browser**: `chrome` \| `firefox` \| `curl` (`chrome-full` is experimental, not for client UIs). Only with `ip=quic`. Selects the ClientHello TLS fingerprint and the Initial frame layout (see below) |

The decoy is sent before the handshake, exactly like a hand-written `i1`. Each
profile is a **client-initiated** packet shaped like that protocol (the shapes are
inspired by the open-source WireSock reference, but emitted as the client request a
peer actually sends first, not a server response):

- **`quic`** — a **QUIC Initial (RFC 9001)** carrying a whole ClientHello (your
  `id` as the SNI) with the fingerprint of the `ib` browser, its frames laid out as
  that stack does. `chrome` — Chrome 155 without the PQ key share, 1250 bytes, the
  ClientHello cut into 2–11 CRYPTO frames with 2–10 PINGs and scattered PADDING
  (`QuicChaosProtector`); `chrome-full` — Chrome 155 with `X25519MLKEM768`, one
  ~2 KB Initial above the MTU (two IP fragments); `firefox` and `curl`/empty — one
  CRYPTO frame plus PADDING. Layout and DCID/random are fresh per call (no cross-user
  signature). `ip=quic` emits **one** Initial in `i1`: a ClientHello spread over
  several Initials is dropped on the way to WARP, while frame order inside one packet
  does not matter (field runs). A plain QUIC short header was blocked.
- **`dns`** — a client DNS **query** (QR=0, QTYPE HTTPS/type 65) whose QNAME is your
  `id`, with a plain EDNS OPT record (no options), the shape a stub resolver sends.
- **`stun`** — a WebRTC/ICE **STUN Binding Request** (magic cookie + USERNAME +
  ICE-CONTROLLING + PRIORITY + SOFTWARE + MESSAGE-INTEGRITY + FINGERPRINT). This is
  a client connectivity-check — the packet an ICE agent legitimately sends first.
- **`sip`** — a body-less SIP **INVITE request** (`i1`: request-line + Via /
  Max-Forwards / To / From / Call-ID / CSeq / Contact + `Content-Type:
  application/sdp` and `Content-Length: 0`, no SDP body), using your `id` (or a
  generated pseudo-host) as the host and pronounceable pseudo user names. `i2`
  stays empty: the `100 Trying` is the server's reply, not a client packet.

**Where `id` reaches the wire:**

| `ip` | decoy | `id` visible to a middlebox? |
|------|-------|---------------------------|
| `quic` | fragmented QUIC Initial, `id` = **SNI** | **yes** — a DPI that decrypts the Initial (keys derive from the DCID) can read it if it reassembles the frames in order |
| `dns` | EDNS query (QR=0), `id` = **QNAME** | **yes**, in the clear |
| `sip` | SIP INVITE, `id` = **host** in the URI | **yes** (when set), in the clear |
| `stun` | STUN Binding Request | **no** — STUN has no field for a hostname |

**Which profile to pick:**

Which profile to pick, and why only `quic` passed on a device:
[amneziawg-explained §3.3–§3.4](amneziawg-explained.md#33-masquerade-sugar-id--ip--ib).

### Notes & limitations

- `id`/`ip`/`ib` are **mutually exclusive** with an explicit `i1` — set one or the
  other (a config with both is rejected).
- `ib` selects the decoy's TLS fingerprint: `chrome`/`firefox` emit a genuine browser
  ClientHello only in builds with TLS-mimicry support; `curl` and an absent `ib` use
  the generic ClientHello. Without that build support the browser profiles fall back
  to the generic one.
- Field status: `ip=quic` is device-proven against real LTE DPI; for
  `dns`/`stun`/`sip`, engine acceptance, structural validity and `sing-box check` are
  confirmed, a field A/B was not run.

**📖 [Detailed examples →](../SPECS/TASKS/009-WIRESOCK_MASQUERADE_PROFILES/EXAMPLES.md)** —
full per-profile configs (incl. a Cloudflare WARP one), the generated CPS for each,
and the exact validation errors.

## 2.6 MTU budget

`s4` prepends junk to **every** transport (data) message, so an AWG endpoint needs a
lower `mtu` than plain WireGuard (`s3` pads only cookie-reply messages and does not
affect the budget). A too-high `mtu` does not stop the handshake but breaks data
transfer:

```
peer(…) - received handshake response
peer(…) - failed to send data packets: write udp4 …: sendmsg: message too long
```

Budget against a 1500-byte path:

```
mtu ≤ 1500 − 28 (UDP/IP) − 32 (WireGuard) − S4 junk bytes
```

For `S4 = 60` that is `mtu ≤ 1380`; the AmneziaWG-recommended client MTU is **1280**
(headroom for PPPoE and nested tunnels).

**What the core does by itself:**

- `mtu` omitted, `s4` set → default **`1280`** (instead of the plain-WireGuard `1408`).
- `mtu` set explicitly and over budget → a startup warning, computed against a
  conservative **1492** (PPPoE): `mtu ≤ 1492 − 28 − 32 − S4`. The tunnel still loads.
- The outer socket **does not force DF** (SPEC 028): an oversize outer datagram is
  IP-fragmented, not dropped; this is what lets nested tunnels work
  (`masque`/`wireguard`/AWG through `detour`). The old behaviour on a specific
  endpoint: `"udp_fragment": false`. A correct `mtu` is still preferred: fragmentation
  is the safety net.
- Keep `jmax` **below** the real path MTU: a junk packet longer than the MTU gets
  IP-fragmented, and constrained paths drop the fragments.

Why it is this way and what it looks like to the user:
[amneziawg-explained §5](amneziawg-explained.md#5-mtu-where-the-bytes-go).

## 2.7 Mapping an `awg.conf` 1:1

Map an `awg.conf` / awg-quick file directly:

| `awg.conf` line | JSON |
|-----------------|------|
| `[Interface] PrivateKey / Address / MTU` | endpoint root `private_key` / `address` / `mtu` |
| `[Interface] Jc / Jmin / Jmax / S1–S4 / I1–I5` | endpoint root `jc` / `jmin` / `jmax` / `s1`–`s4` / `i1`–`i5` |
| `[Interface] H1 = N` | endpoint root `"h1": N` (JSON **number**) |
| `[Interface] H1 = N-M` (AWG2 export) | endpoint root `"h1": "N-M"` (JSON **string**, verbatim) |
| `[Interface] HeaderProtectionKey = <base64>` (AWG3) | endpoint root `"header_protection_key": "<base64>"` (verbatim) |
| `[Interface] ContentPaddingAddition / RekeyAfterTime / RekeyTimeout / RejectAfterTime / KeepaliveTimeout / MaxHandshakeAttempts = N-M` (AWG3) | endpoint root `content_padding_addition` / `rekey_after_time` / `rekey_timeout` / `reject_after_time` / `keepalive_timeout` / `max_handshake_attempts` — `"N-M"` string (or a number `N`) |
| `[Interface] RandomTrailers / DisableCookies = on` (AWG3) | endpoint root `"random_trailers": true` / `"disable_cookies": true` |
| `[Peer] PublicKey / PresharedKey` | `peers[0].public_key` / `pre_shared_key` |
| `[Peer] Endpoint host:port` | `peers[0].address` + `peers[0].port` |
| `[Peer] AllowedIPs / PersistentKeepalive` | `peers[0].allowed_ips` / `persistent_keepalive_interval` (`N`, or `"N-M"` for an AWG3 range) |

If the `awg.conf` omits `MTU` or sets the WireGuard-default `1420`, lower it for
AWG2 (see [§2.6](#26-mtu-budget)).

The Amnezia app's `vpn://` export carries the same keys (`awg` → `last_config` →
`config` is the `.conf` text above); `protocol_version: "3.1"` there marks an AWG3
server.

## 2.8 Examples

### AmneziaWG 2.0 endpoint (hand-written I-packets)

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
  // single values (AWG 1.x) — or ranged AWG 2.0 headers, e.g.
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

### AmneziaWG 3.1 endpoint (`amnezia-awg2` export, `protocol_version` 3.1)

The full parameter set of a live AWG 3.1 server, copied 1:1 from its `.conf`
(`H1`–`H4` stay at the WireGuard defaults — with header protection the type word is
masked anyway). Verified end-to-end against that server (SPEC 080).

```jsonc
{
  "type": "wireguard",
  "tag": "awg3-out",
  "system": false,
  "mtu": 1376,
  "address": ["10.8.1.7/32"],
  "private_key": "<client-private-key-base64>",

  "jc": 4, "jmin": 10, "jmax": 50,
  "s1": 55, "s2": 42, "s3": 40, "s4": 12,          // each >= 12: they carry the header-cipher nonce
  "h1": 1, "h2": 2, "h3": 3, "h4": 4,

  "header_protection_key": "<HeaderProtectionKey-base64>",   // server-side: must equal the server's
  "content_padding_addition": "10-100",
  "rekey_after_time": "100-120",
  "rekey_timeout": "3-7",
  "reject_after_time": "150-180",
  "keepalive_timeout": "5-15",
  "max_handshake_attempts": "15-20",
  "random_trailers": true,
  "disable_cookies": true,

  "peers": [
    {
      "address": "77.239.123.44",
      "port": 30565,
      "public_key": "<server-public-key-base64>",
      "pre_shared_key": "<preshared-key-base64>",
      "allowed_ips": ["0.0.0.0/0", "::/0"],
      "persistent_keepalive_interval": "25-35"    // AWG3 range; a plain number still works
    }
  ]
}
```

### Masquerade sugar (Cloudflare WARP, QUIC profile)

```jsonc
{
  "type": "wireguard", "tag": "awg-out", "mtu": 1280,
  "address": ["172.16.0.2/32", "2606:4700:110:8000::2/128"],
  "private_key": "<client-private-key-base64>",
  "jc": 4, "jmin": 40, "jmax": 70,
  "id": "www.google.com", "ip": "quic", "ib": "chrome",
  "peers": [ { "address": "engage.cloudflareclient.com", "port": 2408,
    "public_key": "<server-public-key-base64>", "allowed_ips": ["0.0.0.0/0", "::/0"],
    "persistent_keepalive_interval": 25 } ]
}
```

## 2.9 Validation errors (verbatim)

| Config | Error |
|--------|-------|
| any AWG field, built without `with_awg` | `AmneziaWG (awg) support is not included in this build, rebuild with -tags with_awg` |
| ranged `h1`–`h4` that overlap | `headers must not overlap` |
| `i1` **and** `id`/`ip`/`ib` together | `amneziawg: id/ip/ib masquerade conflicts with an explicit i1; use one or the other` |
| `id`/`ib` set, `ip` missing | `amneziawg: ip (masquerade protocol) is required when id/ib is set; one of quic\|dns\|stun\|sip` |
| `ip` not in the set | `amneziawg: unknown masquerade protocol "ftp"; one of quic\|dns\|stun\|sip` |
| `ip=quic` without `id` | `amneziawg: id (masquerade domain) is required for ip=quic (it becomes the ClientHello SNI)` |
| `ip=dns` / `ip=sip` / `ip=stun` without `id` | **not an error** — a pseudo name is generated (`dns`/`sip`); `stun` needs none |
| domain with `\r\n` / `;` / `@` / space | `amneziawg: invalid masquerade domain "...": illegal character (only a-z A-Z 0-9 - _ allowed)` |
| `ib` not in the set | `amneziawg: unknown masquerade browser "safari"; one of chrome\|firefox\|curl` |
| `ib` with `ip≠quic` | `amneziawg: ib (browser) is only meaningful with ip=quic, got ip="dns"` |
| `header_protection_key` with any of `s1`–`s4` below 12 | `amneziawg: s4=8 is too short for header_protection_key: each of s1-s4 must be at least 12 bytes (the padding carries the header cipher nonce)` |
| `header_protection_key` not base64 / not 32 bytes / all zeros | `amneziawg: decode header_protection_key (expected base64, as printed by \`awg genkey\`)` / `… must decode to 32 bytes, got N` / `… is all zeros (the device treats that as "off"); omit the field instead` |
| a ranged field with start > end, or garbage | `invalid range "180-150": range start > end` (the offending key is named: `reject_after_time: …`) |
| `persistent_keepalive_interval` range, built without `with_awg` | `AmneziaWG (awg) support is not included in this build (persistent_keepalive_interval range), rebuild with -tags with_awg` |

The domain passes **strict LDH validation** (like a WireSock SNI): labels of
`a-z A-Z 0-9 - _`, ≤63 bytes, no leading/trailing hyphen; the whole name ≤253, no
leading dot; one trailing dot allowed. This is a security boundary — the domain goes
into SIP text and a DNS QNAME, so control bytes and metacharacters are rejected
(injection defense).

## 2.10 AWG 3.x: header protection, padding, trailers, timings

AmneziaWG 3.0 (amneziawg-go v3.0.0, tools v3.0.20260730) and 3.1 (v3.1.2026081x)
added a second layer on top of the AWG2 shape tricks. All of it is configured on the
endpoint root like the AWG2 fields and needs `with_awg`. Amnezia's `amnezia-awg2`
container with `protocol_version: "3.1"` exports exactly this set.

**Which side must match.** AmneziaWG distinguishes *server-side* parameters (both ends
must hold the same value — a mismatch means no handshake, silently) from *client-side*
ones (local behaviour, the server does not care). Only `header_protection_key` is
server-side here; everything else is client-side — but copy the server's values anyway,
they were tuned together.

| Key | Type | Default | Side | Meaning |
|-----|------|---------|------|---------|
| `header_protection_key` | base64 string (32 bytes, `awg genkey`) | `""` (off) | **server** | Encrypts the low-entropy header of every packet: for a handshake message the whole message, for a data packet the 16-byte header (type, receiver index, counter). ChaCha20 keyed with this key and a per-datagram nonce = the first 12 bytes of the message's random padding — hence **each of `s1`–`s4` must be ≥ 12**. With it on, the `h1`–`h4` magic values become irrelevant (the type word is random on the wire) and AWG3 exports leave them at `1`–`4`. |
| `content_padding_addition` | int \| `"min-max"` | `""` (off) | client | Extra zero bytes appended to the plaintext of **every data packet** (inside the AEAD, so random on the wire), picked from the range per packet, **instead of** WireGuard's pad-to-multiple-of-16. Capped so the datagram never exceeds the largest datagram already seen on this peer's path (the "UDP window", starting at 500 B) — a full-MTU packet gets no addition. |
| `random_trailers` | bool | `false` | client | A random-length random tail after every **handshake** message (init/response/cookie) and, via the same UDP-window rule, inside data packets when no `content_padding_addition` is set — so no message kind has a fixed size. The receiver accepts handshake datagrams *longer* than expected and discards the tail. |
| `disable_cookies` | bool | `false` | client | Never send a cookie reply and skip the under-load mac2 gate that would demand one (a cookie exchange has a recognisable shape). |
| `rekey_after_time` | int \| `"min-max"` seconds | `""` (WireGuard 120) | client | When the initiator starts a fresh handshake. Picked from the range at each check. |
| `rekey_timeout` | int \| `"min-max"` seconds | `""` (WireGuard 5) | client | Spacing between handshake retransmits (jitter is still added). The range **minimum** is the floor between two initiations. |
| `reject_after_time` | int \| `"min-max"` seconds | `""` (WireGuard 180) | client | After this a keypair is refused. The range **maximum** is used wherever the protocol must not expire keys early. |
| `keepalive_timeout` | int \| `"min-max"` seconds | `""` (WireGuard 10) | client | Passive keepalive after received data. |
| `max_handshake_attempts` | int \| `"min-max"` | `""` (WireGuard 18) | client | Retransmits before giving up on a handshake cycle (then SPEC 041's self-heal rebind runs as before). Re-picked per cycle. |
| `peers[].persistent_keepalive_interval` | int \| `"min-max"` seconds | `0` (off) | client | The WireGuard number, or an AWG3 range re-picked at every arming. |

Notes:

- The header-protection nonce comes from the **padding**, so on a plain-size handshake
  (`s1`=0) the key cannot be used at all — the config check refuses it
  ([§2.9](#29-validation-errors-verbatim)), as does the device.
- `content_padding_addition` and `random_trailers` grow packets: the MTU budget of
  [§2.6](#26-mtu-budget) is unchanged (the UDP-window cap keeps additions inside sizes
  the path already carried), but keep `mtu` at the server-recommended value (`1376` in
  the Amnezia export).
- `random_trailers` widens the receiver's classification: datagrams longer than
  `s1`+148 / `s2`+92 / `s3`+64 are also tried as a handshake by their type word. Our
  receiver is immune to the false matches (SPEC 081); the server's receiver is the
  reference implementation, so **do not combine `random_trailers` with wide
  `h1`–`h4` ranges**: the uplink would lose packets. Mechanics:
  [amneziawg-explained §6.3](amneziawg-explained.md#63-receive-packet-classification).
- The timing overrides don't need to match the server, but nonsense (e.g.
  `rekey_after_time` above `reject_after_time`) makes the tunnel flap. Copy the
  server's export.
- Everything here is byte-compatible with amneziawg-go v3.1 (commit `b5928ef`,
  2026-08-28): the same nonce/keystream layout, the same classification order, the
  same padding/trailer rules. Verified against a live `protocol_version 3.1` server —
  [SPEC 080](../SPECS/TASKS/080-AWG3_HEADER_PROTECTION_TIMINGS/SPEC.md).

---

# 3. MASQUE outbound (CONNECT-IP / WARP)

> 🧭 How MASQUE/WARP is built and the decisions behind it: the feature spec [009-MASQUE_WARP](../SPECS/FEATURES/009-MASQUE_WARP/FEATURE.md). This chapter holds only fields, defaults and errors.

## 3.1 What it is

A `masque` outbound tunnels **whole IP packets** over an HTTP/3 or HTTP/2 connection
using **CONNECT-IP (RFC 9484)**, primarily to connect to **Cloudflare WARP**. (This
is CONNECT-IP, not CONNECT-UDP / RFC 9298; and unrelated to the AWG `id/ip/ib`
*masquerade* sugar in [§2.5](#25-masquerade-sugar-id--ip--ib) — same word, different
feature.) The core builds a userspace gVisor stack per tunnel and routes traffic
through it like a WireGuard endpoint. Gated on `with_quic` + `with_gvisor` (both in
the default `LX_TAGS`). Key material is taken ready from config — device
registration (ECDSA keys, WARP enroll) is done by the client, not the core.

> A top-level `dns` block is **required** — the userspace stack works at L3 and does
> not resolve domains itself; the outbound resolves them via the DNS router before
> dialing. Without it, domain traffic through the tunnel fails to resolve
> (`masque: no DNS router available to resolve …`).

> ⚠️ **The HTTP version is `vhttp`, not `network`** (SPEC 062). Older configs used
> `network` for `h3`/`h2` — the opposite of what `network` means on every other
> outbound. The old shape still works and reports a deprecation; see the
> [migration table](#310-migrating-from-the-pre-spec-062-shape).

## 3.2 MASQUE-specific fields

| Key | Type | Required | Default | Meaning |
|-----|------|----------|---------|---------|
| `type` | string | ✅ | — | always `"masque"` |
| `tag` | string | ✅ | — | outbound name (for route/groups) |
| `server` | string | ✅ | — | IP/host of the WARP endpoint |
| `server_port` | uint16 | ✅ | — | port (usually 443) |
| `profile` | string | — | `cloudflare` | `cloudflare` \| `standard` (see [§3.4](#34-profiles--what-profile-switches)) |
| `vhttp` | string | — | `auto` | **HTTP version carrying CONNECT-IP**: `auto` (h3 first, h2 fallback when the QUIC handshake does not complete in 3 s — SPEC 074) \| `h3` (QUIC) \| `h2` (HTTP/2, TCP:443). On `standard` the default quietly means `h3` (no h2 leg there). The tcp/udp list is `network_list`, as everywhere else |
| `private_key` | string (base64) | ✅¹ | — | client EC private key, DER (`x509.ParseECPrivateKey`) |
| `public_key` | string (base64) | ✅¹ | — | endpoint PKIX public key, DER (`x509.ParsePKIXPublicKey`, ECDSA) |
| `ip` | string (CIDR) | ✅² | — | local IPv4 inside the tunnel; a bare address → `/32` |
| `ipv6` | string (CIDR) | ✅² | — | local IPv6 inside the tunnel; a bare address → `/128` |
| `tls` | object | — | — | the **standard** outbound TLS block — `server_name`, `insecure`, `disable_sni`, `fragment`, `record_fragment`, `fragment_fallback_delay`, … Same container every other TLS outbound uses |
| `uri` | string | — | per profile³ | CONNECT-IP request URI |
| `mtu` | int | — | `1280` | userspace-stack MTU. On `h2`, max `16000` (one IP packet = one HTTP/2 DATA frame) |
| `idle_timeout` | duration | — | inherits `lx.masque.idle_timeout`, else off | suspend the tunnel after this long with no traffic (frees the gVisor stack, pumps and QUIC keepalive); the next dial rebuilds it. Absent → the global `lx.masque.idle_timeout` (SPEC 098, [lx-config.md §13](lx-config.md#13-the-lx-root-block-spec-098)); an explicit `0` or a negative value keeps this node up whatever the global default; only a positive value enables suspend |
| `keep_alive_period` | duration | — | `30s` | QUIC keepalive (h3 only). **Negative disables** |
| `network_list` | list | — | tcp+udp | L4 protocols routed through the tunnel |

¹ Required for `profile=cloudflare`; optional for `standard`.
² At least **one** of `ip`/`ipv6` is required.
³ Per-profile defaults — see [§3.4](#34-profiles--what-profile-switches).

## 3.3 Inherited DialerOptions

MASQUE embeds the standard sing-box `DialerOptions`, applied to the outgoing dial to
the WARP endpoint. All optional:

| Key | Type | Meaning |
|-----|------|---------|
| `detour` | string | route the dial to the endpoint through another outbound (nested tunnels) |
| `bind_interface` | string | bind to a network interface |
| `inet4_bind_address` / `inet6_bind_address` | addr | outgoing bind address |
| `routing_mark` | int | fwmark (Linux) |
| `reuse_addr` | bool | SO_REUSEADDR |
| `connect_timeout` | duration | connection-establishment timeout |
| `tcp_fast_open` | bool | TFO (relevant on `h2`) |
| `domain_resolver` | object | which DNS resolves `server` when it is a domain |
| `domain_strategy` | string | `prefer_ipv4` \| `prefer_ipv6` \| `ipv4_only` \| `ipv6_only` |
| `fallback_delay` | duration | happy-eyeballs delay |
| `network_strategy` / `network_type` / `fallback_network_type` | | multi-network strategies |

(Full list — the shared `DialerOptions` in `option/outbound.go`.)

## 3.4 Profiles — what `profile` switches

The profile varies four things; everything else (QUIC/HTTP2, capsule/datagram, the
userspace stack, the pumps) is profile-independent.

| Aspect | `cloudflare` (default) | `standard` (RFC 9484) |
|--------|------------------------|-----------------------|
| `:protocol` (h3) / `cf-connect-proto` (h2) | `cf-connect-ip` | `connect-ip` |
| Extended CONNECT settings | tolerate absence (WARP does not send it) | require |
| default `tls.server_name` | `www.cloudflare.com` | = `server` |
| default `uri` | `https://cloudflareaccess.com` | none (required) |
| TLS validation | pin `public_key` (ECDSA) | normal chain by `server_name` |
| `private_key`/`public_key` | required | optional |

`standard` targets a self-hosted RFC-compliant CONNECT-IP server; it **will not
connect to Cloudflare WARP**. For WARP always use `cloudflare` (the default). Note
`vhttp: h2` is **not implemented for `standard`** — a load-time error.

## 3.5 Key material & value formats

**Keys** (`private_key` / `public_key`): base64 of DER. `private_key` =
`x509.MarshalECPrivateKey` (SEC1 EC), `public_key` = `x509.MarshalPKIXPublicKey` of
an `*ecdsa.PublicKey` (P-256) — exactly the format the WARP registration produces on
the client side, parsed by the core without conversion.

**`ip` / `ipv6`**: an address or a CIDR. `"172.16.0.2"` → `172.16.0.2/32`,
`"2606:…::"` → `/128`. These are the **local** addresses **inside** the tunnel (your
address in the WARP network), **not** the exit IP.

**Duration** (`idle_timeout`, `keep_alive_period`, `connect_timeout`, …): a Go
duration string — `"30s"`, `"5m"`, `"1h30m"`. Empty = default. Negative (`"-1s"`) =
"disable".

## 3.6 SNI strategy

> **The default SNI is `www.cloudflare.com`, not the endpoint hostname.** Naming the
> MASQUE endpoint in the ClientHello is exactly what a DPI filters on; a neutral
> high-traffic host is not. The endpoint is authenticated by pinning `public_key`,
> so the SNI is free to differ. `tls.disable_sni: true` sends none at all — some
> endpoints only present their real certificate to a ClientHello without one.

Override `tls.server_name` per node (LxBox rotates a pool). The endpoint's own name
(`consumer-masque.cloudflareclient.com`) is deliberately **not** the default —
sending it was measured to trip a silent CONNECT-IP timeout on Russian uplinks,
while `www.cloudflare.com`, `yandex.ru`, `www.google.com` and other neutral names
all connect to the same endpoint.

## 3.7 `vhttp`: auto / h3 / h2

`auto` is the default (SPEC 074): it tries `h3` (QUIC) first and falls back to `h2`
when the QUIC handshake does not complete within 3 s, remembering the winner; a remembered
leg that stops coming up is forgotten and the other one is tried in the same dial. The failure mode it exists for produces **no error** — the
endpoint (or a TCP-only hop in front of it: an HTTP CONNECT detour, a VLESS/Trojan
link in a chain) silently swallows QUIC and every dial just hangs. A fixed `h3` is
the fastest when the path is known-clean; on networks that filter inbound UDP:443
pin the node to `h2` (TCP:443), which is device-verified to work there.

- On `h2`, one IP packet = one HTTP/2 DATA frame, so `mtu` may go up to **16000**.
- The `h2` path runs its TLS through the shared `common/tls` layer, so it gets
  ClientHello fragmentation like any other TLS outbound — including the automatic
  one under `detour` (SPEC 060). `h3` is untouched by that: QUIC does not carry TLS
  over TCP at all.
- The **first** `h3` dial is slow (cold CONNECT-IP setup: QUIC handshake + Extended
  CONNECT + route advertise + stack), so a short urltest timeout may mark a fresh h3
  node `-1` on the first probe though it works after.

To pin a config to one leg, set the single field: `"vhttp": "h3"` or `"vhttp": "h2"`.

## 3.8 Idle-suspend & keepalive

- `idle_timeout` suspends the tunnel after that long with no traffic, freeing the
  gVisor stack, the pumps and the QUIC keepalive; the next dial rebuilds it.
  **Off by default** (since lx.31): absent, `0` and negative all keep the tunnel up
  until the outbound is closed; only a positive value (e.g. `"5m"`) enables suspend.
  Waking a suspended tunnel costs a full QUIC handshake + CONNECT-IP + a fresh gVisor
  stack on the first request after the quiet spell, which on routers and desktops is
  a worse trade than the ~6 MB RSS and one keepalive packet per 30s it saves. Enable
  it explicitly on battery-powered hosts where that trade flips.
- `keep_alive_period` (default `30s`, h3 only) is the QUIC keepalive interval. A
  negative value disables it.
- **Interaction:** with suspend off, keepalive is what holds the tunnel through the
  server's idle-timeout and the provider's UDP NAT mapping — do **not** disable it
  (`-1s`) unless `idle_timeout` is short enough that the tunnel is torn down before
  keepalive matters; otherwise the server drops the tunnel on its own idle and the
  next dial pays a silent rebuild.
- The exit IP **changes** after idle-suspend/reconnect — WARP anycast hands out a
  different edge address on each new connection. The internal `ip`/`ipv6` stays
  stable.

## 3.9 Start-time validation (fail-fast)

The config is rejected at load if:

- `profile=cloudflare` but `private_key`/`public_key` is missing or unparsable →
  `masque: private_key and public_key are required for the cloudflare profile` /
  `parse private_key` / `parse public_key`;
- neither `ip` nor `ipv6` is set → `masque: at least one of ip/ipv6 is required`;
- `vhttp` ∉ {`h3`, `h2`, `auto`} (e.g. the habitual `"tcp"`) →
  `masque: invalid vhttp: … (expected h3, h2 or auto)`;
- `vhttp=h2` and `mtu > 16000` → `masque: mtu … too large for h2 (max 16000)`;
- `vhttp=h2` and `profile=standard` →
  `masque: vhttp h2 is not implemented for the standard profile`;
- `profile=standard` without `uri` → `masque: uri is required for the standard profile — set it to the server's CONNECT-IP request URI, e.g. https://<host>/.well-known/masque/ip/*/*/` (checked before `vhttp`, so the text is the same for h2/h3/auto — SPEC 091);
- `public_key` is not an ECDSA key → `public_key is not an ECDSA key`.

## 3.10 Migrating from the pre-SPEC-062 shape

Both shapes are accepted until **v1.14.0-lx.30**; using a legacy field logs one
deprecation line per outbound. A legacy field that *disagrees* with its replacement
is a hard error rather than a silent pick.

| Legacy (deprecated) | Current |
|---|---|
| `network: "h3"` / `"h2"` | `vhttp: "h3"` / `"h2"` |
| `sni` | `tls.server_name` |
| `skip_cert_verify: true` | `tls.insecure: true` |
| `fragment: true` | `tls.fragment: true` |
| `fragment_fallback_delay` | `tls.fragment_fallback_delay` |
| `record_fragment: true` | `tls.record_fragment: true` |

> The legacy booleans cannot tell "absent" from an explicit `false`, so only a
> legacy `true` carries over — write the `tls` form if you need to turn something
> off.

## 3.11 Examples

### WARP over h3 (QUIC)

```jsonc
{
  "type": "masque",
  "tag": "warp",
  "server": "162.159.198.2",
  "server_port": 443,
  "profile": "cloudflare",
  "vhttp": "h3",
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

### WARP over h2 (CONNECT-IP over TCP:443)

Same as above with one field changed:

```jsonc
{ /* … */ "vhttp": "h2", "mtu": 1280 }
```

### Minimal config (only what is required)

```jsonc
{
  "type": "masque",
  "tag": "warp",
  "server": "162.159.198.2",
  "server_port": 443,
  "private_key": "<base64 DER EC private key>",
  "public_key":  "<base64 DER PKIX public key>",
  "ip":   "172.16.0.2",
  "ipv6": "2606:4700:110:...::"
}
```

(profile=cloudflare, vhttp=h3, `tls.server_name`=`www.cloudflare.com`,
uri=`https://cloudflareaccess.com`, mtu=1280, idle_timeout=off,
keep_alive_period=30s, network_list=tcp+udp — all default. Remember the top-level
`dns` block.)

## 3.12 Common footguns

1. **`vhttp` ≠ tcp/udp.** On `masque`, `vhttp` is the HTTP version (h3/h2). The
   tcp/udp list is `network_list`. `"vhttp": "tcp"` is a fail-fast error. Every
   other outbound uses `network` the opposite way — here it is `vhttp` by design.
2. **Top-level `dns` block is required** — otherwise domain traffic through the
   tunnel does not resolve.
3. **The exit IP changes** after idle-suspend/reconnect — WARP anycast hands out
   different edge addresses per new connection. The internal `ip`/`ipv6` stays
   stable.
4. **`tls.insecure: true`** removes public-key pinning entirely — the only defense
   left when WARP SNI is masqueraded. Debug only.
5. **`keep_alive_period` vs `idle_timeout`** — see [§3.8](#38-idle-suspend--keepalive).

**📖 Status.** Device-verified end-to-end on real Wi-Fi and LTE — `warp=on`, real
traffic on both `h3` and `h2`, idle-suspend + self-healing reconnect confirmed
on-device.

---

# 4. gRPC transport

> 🧭 An upstream transport with no explanatory section; only the `service_name` forms where our behaviour differs from upstream are listed here.

The gRPC transport (`"transport": { "type": "grpc" }`) needs no build tag — the
lite implementation (`v2raygrpclite`) is what ships in our builds; the full
`grpc-go` one is behind `with_grpc` and is not in the desktop/CLI binary.

## 4.1 `service_name`: the Xray forms

`service_name` carries **two** forms, exactly as in Xray. Which one you get is
decided by a single character: a leading `/`.

- **No leading `/`** — the whole value is one service segment: it is escaped as a
  whole (`/` inside it becomes `%2F`) and the stream name is always `Tun`. This
  is the historical form and is byte-for-byte unchanged.
- **Leading `/`** — the value is a **custom path** in Xray's convention: every
  segment is escaped **separately** (so `/` stays a real path separator), the
  **last** segment is the stream name, and a `|…` tail on the stream name (Xray's
  multi-stream name) is dropped.

| `service_name` | Path on the wire | Service / stream | Note |
|---|---|---|---|
| `TunService` | `/TunService/Tun` | `TunService` / `Tun` | historical form |
| `a/b` | `/a%2Fb/Tun` | `a%2Fb` / `Tun` | historical form, = Xray's old form |
| `/a/b/Tun` | `/a/b/Tun` | `a/b` / `Tun` | custom path, = Xray |
| `/a/b/Stream` | `/a/b/Stream` | `a/b` / `Stream` | stream name comes from the config |
| `/a b/Tun` | `/a%20b/Tun` | `a%20b` / `Tun` | per-segment `PathEscape` |
| `/a/b/Tun\|TunMulti` | `/a/b/Tun` | `a/b` / `Tun` | multi tail dropped, as Xray does |

There is no new option and no validation: the form is chosen by the content of
`service_name`, the way Xray does it.

**Notes.**

1. The **`with_grpc`** client does **not** escape the path for the historical
   form (it sends `"/" + name + "/Tun"` raw, so `a/b` goes out as `/a/b/Tun`).
   That is upstream behaviour and we did not change it — the difference only
   shows up for a historical-form name that itself contains `/`.
2. Our **lite server** compares the **decoded** path, so it is more lenient than
   Xray: a server configured with `service_name: "a/b"` accepts both
   `/a%2Fb/Tun` and `/a/b/Tun`. An Xray server compares strictly, so pick the
   same form on both ends.
3. **Symptom of a form mismatch** against an Xray server:
   `v2ray-grpc: unexpected status: 404 Not Found` — the dial reaches the server
   and is rejected on the path.

**📖 Status.** Implemented and covered by unit and client↔server tests; **not yet
run against a live Xray** — the wire path was checked against the Xray and grpc-go
source. Available since `v1.14.1-lx.8`.

---

# 5. REALITY and VLESS `encryption`

> 🧭 How REALITY recognises its clients, what the hybrid key share is, how the `mlkem768x25519plus` layer works and why: [xray-protocols-explained §1–§2](xray-protocols-explained.md#1-foundation-tls-clienthello-and-the-fingerprint) and [§5](xray-protocols-explained.md#5-vless-encryption-the-post-quantum-layer); typical REALITY failures are there too, [§2.7](xray-protocols-explained.md#27-typical-failures). This chapter holds only fields, defaults and errors.

Two independent layers. REALITY lives in the `tls` block of any TLS-over-TCP outbound
(VLESS, trojan, vmess, the XHTTP transport) and needs `with_utls`. `encryption` is a
flat field on a `vless` outbound, beside `uuid`, with no build tag. The upstream `tls`
fields (`server_name`, `alpn`, `insecure`, `ech`, …) are described in the
[upstream TLS docs](../docs/configuration/shared/tls.md); below is only what the fork
added or changed.

## 5.1 `tls.reality` fields

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `enabled` | bool | `false` | Use REALITY instead of regular certificate verification ([upstream](../docs/configuration/shared/tls.md#reality-fields)) |
| `public_key` | base64url, 32 bytes | — | The server's X25519 public key (Xray `publicKey`) |
| `short_id` | hex, up to 8 bytes | — | Client tag (Xray `shortId`) |
| `key_share` | `""` \| `"hybrid"` \| `"classical"` | `""` | Key share form in the ClientHello (SPEC 089, [§5.2](#52-fingerprints-and-the-hybrid-key-share)) |

| `key_share` | ClientHello | Works against |
|---|---|---|
| `""` | whatever the fingerprint carries ([§5.2](#52-fingerprints-and-the-hybrid-key-share)) | as before |
| `"classical"` | `X25519MLKEM768` removed from `key_share` and `supported_groups`; `chrome`: 594 bytes instead of 1720, one TCP segment instead of two | **Xray < v26.9.8 only**; newer servers reject silently (`reality verification failed`) |
| `"hybrid"` | the hybrid is required; on a fingerprint without it the handshake fails with a message ([§5.4](#54-reality-validation-errors-verbatim)) | as `""` |

There is no automatic fallback between `hybrid` and `classical`. The Xray fields
`spiderX`, `mldsa65Verify` and `realitySettings.password` are not accepted by the core.

## 5.2 Fingerprints and the hybrid key share

`tls.utls.fingerprint` is an upstream key (enum: `chrome`, `chrome_psk`,
`chrome_psk_shuffle`, `chrome_padding_psk_shuffle`, `chrome_pq`, `chrome_pq_psk`,
`firefox`, `edge`, `safari`, `360`, `qq`, `ios`, `android`, `random`, `randomized`;
plus our `chrome_155`, SPEC 118), but under REALITY it decides the node's fate: Xray ≥ v26.9.8 accepts only a ClientHello
with the `X25519MLKEM768` hybrid share before `X25519` (SPEC 083). The core does **not**
substitute the fingerprint.

| `fingerprint` | Share in the ClientHello | Xray ≥ v26.9.8 | Preset source |
|---|---|---|---|
| `chrome` and the upstream `chrome_*` | GREASE, **X25519MLKEM768**, X25519 | passes | metacubex/utls, Chrome 133 |
| `chrome_155` | GREASE, **X25519MLKEM768**, X25519 | passes | utls-lx fork, Chrome 155 (SPEC 118): opt-in, adds ML-DSA signature schemes and `trust_anchors`; `chrome` stays 133 |
| `firefox` | **X25519MLKEM768**, X25519 | passes | utls-lx fork, Firefox 148 (SPEC 086) |
| `safari` | **X25519MLKEM768**, X25519 | passes | utls-lx fork, Safari 26.3 (SPEC 087) |
| `edge`, `ios`, `android`, `360`, `qq` | X25519 only | rejected | no presets with the hybrid exist |
| `random` | as the drawn preset | 3 of 5 | — |
| `randomized` | hybrid by coin flip | ~½ of processes | — |

`AuthKey` is computed from the key the server will choose: pure X25519 if present in
`key_share`, otherwise the X25519 part of the hybrid. The client version in the session
id is the required minimum (SPEC 053).

## 5.3 ClientHello fragmentation

Upstream `tls` keys; in the fork they also apply to REALITY (SPEC 088).

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `fragment` | bool | `false` | Cut the first packet by SNI into TCP segments, waiting for the ACK ([upstream](../docs/configuration/shared/tls.md#fragment)) |
| `record_fragment` | bool | `false`; **`true` under `detour`** (SPEC 060) | The same cut points, each its own TLS record, one packet ([upstream](../docs/configuration/shared/tls.md#record_fragment)) |
| `fragment_fallback_delay` | duration | `500ms` | The pause used when the ACK wait time cannot be computed ([upstream](../docs/configuration/shared/tls.md#fragment_fallback_delay)) |

Rules of the `detour` default: an explicit `fragment: true` or `record_fragment: true`
beats the default; only the handshake is fragmented; `h3`/QUIC is untouched; every
`chain` link has its own `detour`. An explicit `"record_fragment": false` cannot be
told apart from "unset", so under `detour` the default still turns on. Overview:
[lx-config §9](lx-config.md#9-automatic-clienthello-fragmentation-under-detour-spec-060).

## 5.4 REALITY validation errors (verbatim)

| Config | Error |
|--------|-------|
| `public_key` not base64url | `decode public_key: …` |
| `public_key` not 32 bytes | `invalid public_key` |
| `short_id` longer than 8 bytes | `invalid short_id` |
| `short_id` not hex | `decode short_id: …` |
| `key_share` outside the set | `unknown reality key_share: X (expected "hybrid" or "classical")` |
| `key_share: "hybrid"` on a fingerprint without the hybrid (at handshake) | `reality key_share "hybrid": fingerprint Edge 85 carries no X25519MLKEM768 key share` |
| the server rejected the client (any reason) | `reality verification failed` — not a config error; the causes are in [xray-protocols-explained §2.7](xray-protocols-explained.md#27-typical-failures) |

## 5.5 The `encryption` field on `vless`

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `encryption` | string | `""` | `""` / `"none"`: layer off, upstream behaviour byte for byte. Otherwise a spec string validated at `check`/start (SPEC 032). In Xray this is `users[0].encryption`, carried over verbatim |

Grammar (dot-separated segments):

```
mlkem768x25519plus.<native|xorpub|random>.<0rtt|1rtt>[.<padding>…].<key>[.<key>…]
```

| Segment | Values | Meaning |
|---------|--------|---------|
| method | `mlkem768x25519plus` | the only one; must match the server |
| appearance | `native` \| `xorpub` \| `random` | how the layer looks on the wire: records shaped like TLS 1.3 / plus XOR of the public keys / a fully random stream |
| rtt | `0rtt` \| `1rtt` | a reusable server ticket / a full handshake on every connection |
| padding | `p-min-max` blocks, dot-separated | segments shorter than 20 characters before the first key; even blocks are lengths (probability %, from, to bytes), odd blocks are gaps (probability %, from, to ms); the first block needs `p ≥ 100` and `min`/`max ≥ 35`; the sum of maxima ≤ 65553; `1rtt` only |
| key | base64url | the server's public key: X25519 (32 bytes) or ML-KEM-768 (1184 bytes, ~1579 characters); one or several |

Padding default when no blocks are given: lengths `100-111-1111.50-0-3333`, gaps `75-0-111`.
The server half (`decryption`) is not ported.

## 5.6 `encryption` validation errors (verbatim)

| Config | Error |
|--------|-------|
| empty string after trimming | `empty encryption string` |
| fewer than four segments | `invalid encryption string: expected at least method.appearance.rtt.key, got N segments` |
| method other than `mlkem768x25519plus` | `unsupported encryption method: X (only mlkem768x25519plus exists)` |
| appearance outside the set | `unknown encryption appearance: X (expected native\|xorpub\|random)` |
| rtt outside the set | `unknown encryption RTT mode: X (expected 0rtt\|1rtt)` |
| empty segment (two dots in a row) | `empty segment in encryption string` |
| key not base64url | `invalid encryption key (not base64url): X` |
| key neither 32 nor 1184 bytes | `invalid encryption key length: N (expected 32 or 1184)` |
| no keys at all | `no encryption keys in encryption string` |
| padding block not `a-b-c` | `invalid padding lenth/gap parameter: X` (spelling as in the reference implementation) |
| first padding block below the threshold | `first padding length must not be smaller than 35` |
| sum of padding maxima above the threshold | `total padding length must not be larger than 65553` |

## 5.7 Examples

VLESS + Vision + REALITY over bare TCP:

```jsonc
{
  "type": "vless",
  "tag": "reality-out",
  "server": "203.0.113.10",
  "server_port": 443,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "flow": "xtls-rprx-vision",
  "tls": {
    "enabled": true,
    "server_name": "www.microsoft.com",
    "utls": { "enabled": true, "fingerprint": "chrome" },
    "reality": { "enabled": true, "public_key": "<reality-public-key-base64url>", "short_id": "0123abcd" }
  }
}
```

VLESS + `encryption` over WebSocket without outer TLS:

```jsonc
{
  "type": "vless",
  "tag": "pq-ws",
  "server": "203.0.113.20",
  "server_port": 80,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "encryption": "mlkem768x25519plus.native.0rtt.<base64url ML-KEM-768 key>",
  "transport": { "type": "ws", "path": "/ws" }
}
```

The mapping to the Xray keys (`realitySettings.*`, `users[0].encryption`) with notes:
[xray-protocols-explained §2.8](xray-protocols-explained.md#28-example-xray-and-sing-box-lx)
and [§5.8](xray-protocols-explained.md#58-example-xray-and-sing-box-lx).

---

## See also

- **[lx-config.md](lx-config.md)** — the downstream-features overview these
  chapters expand on (feature table, build tags, plus idle-suspend, DNS group, VLESS
  `encryption`, `lxd`, observability).
- **[lx-energy.md](lx-energy.md)** — the energy model, idle-suspend timelines and the
  recommended mobile configuration (relevant to AWG and MASQUE endpoint suspend).
- **[xray-protocols-explained.md](xray-protocols-explained.md)** — how XHTTP, REALITY,
  Vision and VLESS `encryption` work and how the fork supports them;
  **[amneziawg-explained.md](amneziawg-explained.md)** — the same for AmneziaWG,
  including the MTU budget.
- Feature specs: [XHTTP](../SPECS/FEATURES/002-XHTTP/), [AWG](../SPECS/FEATURES/003-AWG/),
  [MASQUE/WARP](../SPECS/FEATURES/009-MASQUE_WARP/), [REALITY](../SPECS/FEATURES/017-REALITY/),
  [VLESS_ENCRYPTION](../SPECS/FEATURES/012-VLESS_ENCRYPTION/).
