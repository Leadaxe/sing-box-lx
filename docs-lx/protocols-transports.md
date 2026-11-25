# Protocols & transports — full parameter reference

> 🌐 Русская версия: **[protocols-transports.ru.md](protocols-transports.ru.md)**.

> 🧭 **Where to look.** The fork's documentation has three levels, by the reader's question:
> [lx-config](lx-config.md) — what the fork has and how to enable it;
> [protocols-transports](protocols-transports.md) — every field, type, default, error text;
> [xray-protocols-explained](xray-protocols-explained.md), [amneziawg-explained](amneziawg-explained.md) and [masque-explained](masque-explained.md) —
> how it works, why, how the fork does it and how it differs from vanilla.

A reference to every field of the downstream protocol and transport features of
`sing-box-lx`:

| Feature | Build tag | Where it attaches | Chapter |
|---------|-----------|-------------------|---------|
| **XHTTP** transport (Xray "splithttp"/"xhttp") | `with_xhttp` | `transport` block of a VLESS / VMess / Trojan outbound | [§1](#1-xhttp-transport) |
| **AmneziaWG 2.0/3.x** (AWG2, AWG3) obfuscation | `with_awg` | promoted fields on a `wireguard` endpoint | [§2](#2-amneziawg-203x-awg2-awg3) |
| **MASQUE** outbound (CONNECT-IP / WARP) | `with_quic` + `with_gvisor` | `outbounds[].type: "masque"` | [§3](#3-masque-outbound-connect-ip--warp) |
| **REALITY** client (hybrid key share, `key_share`, fragmentation) and **VLESS `encryption`** | — (REALITY inside `with_utls`) | `tls` block of a TLS-over-TCP outbound; flat `encryption` field on `vless` | [§5](#5-reality-and-vless-encryption) |
| **TrustTunnel** outbound and inbound (AdGuard VPN protocol) | `with_trusttunnel` (+ `with_quic` for HTTP/3), in `LX_TAGS` since lx.21, not in the AAR | `outbounds[].type` / `inbounds[].type: "trusttunnel"` | [§6](#6-trusttunnel-outbound-and-inbound) |

[§4](#4-grpc-transport) covers the gRPC transport. It is upstream's; it is in this
reference for the one place with downstream interop notes: the forms of
`service_name`.

A tour of every downstream feature (idle-suspend, DNS group, VLESS `encryption`,
`lxd`, observability) and short "getting started" examples are in
[lx-config.md](lx-config.md). This document is the reference the overview links
to: every field, its type, its default, its validation, and the exact error text
for a wrong value.

Everything here is sourced from the option structs (`option/v2ray_xhttp.go`,
`option/wireguard_awg.go`, `option/masque_lx.go`) and the protocol implementations.
Defaults and error strings are the ones the current core emits.

> ⚠️ Every key / UUID / address below is a placeholder. Never commit real
> private keys or pre-shared keys to a repository.

The desktop/CLI binary bundles all four and is built with one command:

```bash
make -f Makefile.lx lx-build
```

Without the tag the feature is absent, and a config that uses it is rejected at
load time with an explicit error. There is no silent downgrade to a plain
transport: it would defeat the obfuscation. The error texts:

- XHTTP without `with_xhttp` → `XHTTP transport requires the with_xhttp build tag`
- AWG field without `with_awg` → `AmneziaWG (awg) support is not included in this build, rebuild with -tags with_awg`
- `masque` outbound without `with_quic`+`with_gvisor` → the `masque` type is not registered (`unknown outbound type: masque`)
- `trusttunnel` without `with_trusttunnel` → `trusttunnel is not included in this build, rebuild with -tags with_trusttunnel` (only in a custom build without the tag: `lx-build` carries it since lx.21, the Android AAR does not; which builds carry TrustTunnel is in [§6](#6-trusttunnel-outbound-and-inbound))

---

## Table of contents

- [1. XHTTP transport](#1-xhttp-transport)
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
- [2. AmneziaWG 2.0/3.x (AWG2, AWG3)](#2-amneziawg-203x-awg2-awg3)
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
- [3. MASQUE outbound (CONNECT-IP / WARP)](#3-masque-outbound-connect-ip--warp)
  - [3.1 What it is](#31-what-it-is)
  - [3.2 MASQUE-specific fields](#32-masque-specific-fields)
  - [3.3 Inherited DialerOptions](#33-inherited-dialeroptions)
  - [3.4 Profiles — what `profile` switches](#34-profiles--what-profile-switches)
  - [3.5 Key material & value formats](#35-key-material--value-formats)
  - [3.6 SNI strategy](#36-sni-strategy)
  - [3.7 `vhttp`: auto / h3 / h2](#37-vhttp-auto--h3--h2)
  - [3.8 Idle-suspend & keepalive](#38-idle-suspend--keepalive)
  - [3.9 Start-time validation (fail-fast)](#39-start-time-validation-fail-fast)
  - [3.10 Migrating from the old shape](#310-migrating-from-the-old-shape)
  - [3.11 Examples](#311-examples)
  - [3.12 Common footguns](#312-common-footguns)
- [4. gRPC transport](#4-grpc-transport)
  - [4.1 `service_name`: the Xray forms](#41-service_name-the-xray-forms)
- [5. REALITY and VLESS `encryption`](#5-reality-and-vless-encryption)
  - [5.1 `tls.reality` fields](#51-tlsreality-fields)
  - [5.2 Fingerprints and the hybrid key share](#52-fingerprints-and-the-hybrid-key-share)
  - [5.3 ClientHello fragmentation](#53-clienthello-fragmentation)
  - [5.4 REALITY validation errors (verbatim)](#54-reality-validation-errors-verbatim)
  - [5.5 The `encryption` field on `vless`](#55-the-encryption-field-on-vless)
  - [5.6 `encryption` validation errors (verbatim)](#56-encryption-validation-errors-verbatim)
  - [5.7 Examples](#57-examples)
- [6. TrustTunnel outbound and inbound](#6-trusttunnel-outbound-and-inbound)
  - [6.1 What it is](#61-what-it-is)
  - [6.2 Outbound fields](#62-outbound-fields)
  - [6.3 Start-time validation (verbatim)](#63-start-time-validation-verbatim)
  - [6.4 Session behaviour](#64-session-behaviour)
  - [6.5 Outbound key mapping](#65-outbound-key-mapping)
  - [6.6 Inbound fields](#66-inbound-fields)
  - [6.7 Inbound behaviour](#67-inbound-behaviour)
  - [6.8 Inbound key mapping](#68-inbound-key-mapping)
  - [6.9 Examples](#69-examples)

---

# 1. XHTTP transport

> 🧭 How XHTTP works, why, and how the fork implements it: [xray-protocols-explained §4](xray-protocols-explained.md#4-xhttp); symptom-based troubleshooting is there too, [§4.6](xray-protocols-explained.md#46-a-mismatch-with-the-server-shows-up-as-silence-not-as-an-error). This chapter holds only fields, defaults and errors.

XHTTP (Xray "splithttp"/"xhttp") is a v2ray transport that tunnels the proxy over
plain HTTP requests. It uses HTTP/2 by default, and HTTP/1.1 or HTTP/3 when the TLS
block asks for them ([HTTP version](#http-version)). It attaches to
VLESS / VMess / Trojan through the shared `transport` block and composes with TLS,
including Reality. XTLS-Vision runs over XHTTP only when the VLESS outbound also
sets `encryption`: Vision then sits on the encryption layer, not on the
transport's TLS, as in Xray.

JSON keys are snake_case to match Xray's stream settings, sing-box-extended and the
rest of sing-box. With every field below at its default, the wire shape is
byte-identical to the v1 client verified live. Existing configs are unaffected:
every v2 field is opt-in.

## 1.1 Modes

`mode` selects how the request/response streams are shaped (Xray-compatible):

| Mode | Shape |
|------|-------|
| `auto` (default) | resolves to `stream-one` on a Reality TLS, otherwise `packet-up` — the same rule Xray applies |
| `packet-up` | a separate GET download stream + sequential POST upload packets |
| `stream-up` | a single streamed POST upload + a separate GET download stream |
| `stream-one` | a single bidirectional HTTP stream (request body up, response body down) — the closest analogue to httpupgrade |

Leave `mode` at `auto` unless the server documents otherwise. Set it explicitly
only if you know what the server expects.

### HTTP version

There is no separate key: the HTTP version follows from the outbound's `tls`
block, by the same rule as Xray (`decideHTTPVersion`). Every mode works on every
version.

| TLS | Reality | `tls.alpn` | Version | Wire |
|-----|---------|-----------|---------|------|
| off | — | — | HTTP/1.1 | TCP |
| on | on | any | HTTP/2 | TCP + Reality |
| on | off | empty | HTTP/2 | TCP + TLS, ALPN `h2` |
| on | off | two or more entries | HTTP/2 | TCP + TLS, ALPN as configured |
| on | off | `["http/1.1"]` | HTTP/1.1 | TCP + TLS |
| on | off | `["h3"]` | HTTP/3 | UDP + QUIC |
| on | off | any other single entry | HTTP/2 | TCP + TLS, ALPN as configured |

- The entry is compared exactly (`h3`, `http/1.1`).
- **HTTP/3** needs a build with `with_quic` (all shipped builds have it); without
  it a config with `alpn: ["h3"]` fails to load. The QUIC handshake uses the
  Chrome profile. `utls.fingerprint` does not apply over HTTP/3: loading prints a
  warning and the config still loads, as in Xray. With `disable_sni` the Chrome
  profile is turned off for that server, also with a warning. ECH is not
  supported over HTTP/3: the config loads, dials of that server fail.
  HTTP/3 traffic goes over UDP through the same `detour`.
- **HTTP/1.1**: long requests (the download GET, a streamed POST) each get their
  own connection with `Connection: close`. `packet-up` upload POSTs reuse
  keep-alive connections.
- **Reality is always HTTP/2.** A `tls.alpn` without `h2` is replaced with
  `["h2"]` and a warning: otherwise a Reality server with `alpn: ["h3"]` would not
  come up over TCP.
- **Without TLS the client uses HTTP/1.1, as Xray does.** An Xray server accepts
  both HTTP/1.1 and h2c, so both direct cleartext nodes and nodes behind a reverse
  proxy or CDN that only speaks HTTP/1.1 on a cleartext port work. There is no
  multiplexing: every download stream and every streamed request takes its own
  TCP connection.
- Xray `downloadSettings` and `finalmask.quicParams` are not supported. QUIC runs
  with Xray's defaults; congestion control is Cubic, Xray uses BBR.

## 1.2 Core fields (v1)

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `type` | string | — | selector — must be `"xhttp"` |
| `mode` | string | `auto` | see [§1.1](#11-modes) |
| `host` | string | TLS SNI / server address | overrides the HTTP `Host` header |
| `path` | string | `""` (root) | request path prefix; the session id (and, for `packet-up`, the upload sequence number) are appended as path segments when their placement is `path` |
| `headers` | object | — | extra request headers sent on every XHTTP request |
| `x_padding_bytes` | string range | `"100-1000"` | inclusive byte-length range of the padding value (`"min-max"` or a single int). Sets both the legacy Referer `x_padding` length and the obfs-mode padding length |
| `no_grpc_header` | bool | `false` | omit the `Content-Type: application/grpc` header that streamed-body modes (`stream-one`, `stream-up`) send by default; the counterpart of Xray's `NoGRPCHeader`. Reverse proxies and CDNs turn on unbuffered response streaming by the gRPC content type, and without the header a `stream-one` dial can hang until timeout. Leave off unless the server rejects the gRPC content type |

## 1.3 Session / seq placement (v2)

Where the per-request session id and, in `packet-up`, the upload sequence number
are carried. The session id identifies the logical connection across the split
upload and download streams. The seq orders the upload POSTs.

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `session_placement` | string | `path` | `path` \| `query` \| `header` \| `cookie` |
| `session_key` | string | `X-Session` (header) / `x_session` (query\|cookie) | name carrying the session id when placement ≠ `path`; unused for `path` |
| `seq_placement` | string | `path` | `path` \| `query` \| `header` \| `cookie`. For `path`, the seq is the second appended segment |
| `seq_key` | string | `X-Seq` (header) / `x_seq` (query\|cookie) | name carrying the seq when placement ≠ `path`; unused for `path` |
| `session_table` | string | unset | alphabet the random session id is drawn from: a predefined name or a literal ASCII set. Client-only |
| `session_length` | string | unset | session id length, `"min-max"` or `"n"`. Only used together with `session_table` |

By default the session id is a dashed UUID (`8f14e45f-ceea-4d31-9d4f-d0b8e5c1a2b7`),
which an unconfigured Xray peer also sends. `session_table` and `session_length`
replace it with a random string of your choosing: `"session_table": "Base62"` with
`"session_length": "16-32"` yields `k7Qm2XpR9vLdA3wZ`. This drops the recognizable
UUID fingerprint from the URL, so pick a shape that matches the service you are
imitating.

Predefined alphabet names are case-sensitive, and the sets are byte-for-byte
Xray's: `hex`, `HEX`, `number`, `alphabet`, `Alphabet`, `ALPHABET`, `base36`,
`BASE36`, `Base62`. Any other value is taken as a literal ASCII alphabet.

Both options must be set together: setting one alone is rejected, with no fallback
to UUIDs. The length floor must be above 0, and the id space (`len(table) ^ min`)
must exceed 2^31. Otherwise two independent clients could draw the same id and get
merged into one server-side session.

The server is never told the shape: it treats the session id as an opaque grouping
key. The knob is client-only: no server change is needed, and an Xray server
accepts either form. Xray calls these `sessionIDTable` / `sessionIDLength`.

> ⚠️ With both the session id and the seq on `path` (the default), the session id
> is appended as the first segment and the seq as the second, and the server splits
> the path in that order. Do not reorder them by hand.

## 1.4 Uplink-data placement (v2, packet-up)

Where the upload payload goes. The `header` and `cookie` values are valid only in
`packet-up` mode; in another mode they are a load-time error.

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `uplink_data_placement` | string | `auto` | `body` \| `auto` (== body) \| `header` \| `cookie`. `header`/`cookie` carry the payload as `base64.RawURLEncoding`, chunked into `<key>-<i>` headers / `<key>_<i>` cookies |
| `uplink_data_key` | string | `X-Data` (header/auto) / `x_data` (cookie) | base name for the chunked header/cookie payload; `""` for body |
| `uplink_chunk_size` | string range | cookie `2048-3072`, header `3000-4000`, else `= sc_max_each_post_bytes` | `"min-max"` range (in base64 chars) of each chunk; the min is floored to 64 |
| `uplink_http_method` | string | `POST` | HTTP method for upload requests (download is always GET); upper-cased. `GET` is allowed only in `packet-up` |

## 1.5 X-Padding obfuscation (v2)

The obfuscation is active only when `x_padding_obfs_mode` is `true`. Otherwise the
client sends the legacy Referer padding; its format is described in the paragraph
after the examples in [§1.10](#110-examples). The padding length is always set by
`x_padding_bytes` ([§1.2](#12-core-fields-v1)), and the fields below choose where
and how the padding bytes are placed.

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

Without a pool every XHTTP stream goes through a full TCP + TLS (+ REALITY)
handshake. `xmux` reuses HTTP connections, and Xray servers expect it too: the
client applies an `xmux` section from a subscription as the server author intended.

The pool is always on, as in Xray-core: a `nil` or absent `xmux` section enables
XMUX with Xray-compatible defaults. The defaults apply only when the section is
absent or entirely empty. A section with any field set takes every field as written.

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `xmux.max_concurrency` | range | unlimited | how many streams may share one HTTP connection. Mutually exclusive with `max_connections` |
| `xmux.max_connections` | range | `3-3` | how many connections the pool holds; below this count a new connection is always opened. Mutually exclusive with `max_concurrency` |
| `xmux.c_max_reuse_times` | range | unlimited | how many times a connection may be handed out for a new stream before it retires |
| `xmux.h_max_request_times` | range | `600-900` | how many HTTP requests may traverse a connection before it retires. Counts requests, not streams — in `packet-up` one stream issues many upload POSTs |
| `xmux.h_max_reusable_secs` | range | `1800-3000` | how long a connection stays reusable, in seconds |
| `xmux.h_keep_alive_period` | int (seconds) | `0` = default | Keep-alive period. HTTP/2: ping period (the transport's `ReadIdleTimeout`), `0` = no pings. HTTP/3: QUIC keep-alive, `0` = 10 s. No effect on HTTP/1.1. Negative disables keep-alive. A plain integer, not a range, as in the reference implementation |

Each range is rolled once, not per request. The manager rolls `max_concurrency`
and `max_connections` at construction, and every connection rolls its own reuse
limits when it is created. A connection at its limit is retired but not torn down
while streams still use it: closing waits until the last one finishes. Only the
client is implemented: the server half and the `download` section are out of scope.

## 1.8 Accepted-but-ignored fields

The client accepts these fields so an inbound-shaped config or a symmetric link
doesn't error, but never acts on them:

| Key | Why it exists |
|-----|---------------|
| `sc_max_concurrent_posts` | legacy Xray knob (removed from current Xray-core); current Xray serializes to one POST body in flight, matching our sequential upload |
| `server_max_header_bytes` | server-only (`http.Server.MaxHeaderBytes`) |
| `no_sse_header` | server-only (omit `Content-Type: text/event-stream` on the stream-down response); the client never inspects `Content-Type` |
| `sc_max_buffered_posts` | server-only (upload reorder buffer depth) |
| `sc_stream_up_server_secs` | server-only ("min-max" keepalive-padding interval on the stream-up response). The client does not strip server-injected keepalive padding from the stream-up download — verify against the target server if it emits any |

## 1.9 Range value forms

Every field marked "range" (`x_padding_bytes`, `uplink_chunk_size`, `sc_*`, all
`xmux.*` except `h_keep_alive_period`) accepts three forms:

- `"min-max"` string — `"600-900"`
- a single integer — `4` (equivalent to `4-4`) or `"4000"`
- a two-element JSON array — `[600, 900]` (for configs authored against Xray /
  sing-box-extended)

An empty value means the default from the field's table.

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

With `x_padding_obfs_mode` off (the default), padding is carried as
`x_padding=<zeros>` inside the `Referer` header, which is Xray's default placement.
The format is verified live against an Xray (3x-ui) server. The server validates
the `x_padding` length (default 100–1000) and replies `400` without it. Client and
server Xray versions should still match: XHTTP changes quickly.

## 1.11 Troubleshooting

Symptom-based troubleshooting is in the explanatory document:
[xray-protocols-explained §4.6 "Pitfalls where the connection stays silent"](xray-protocols-explained.md#46-a-mismatch-with-the-server-shows-up-as-silence-not-as-an-error).
This reference keeps only the fields: the tables above and the
accepted-but-ignored fields ([§1.8](#18-accepted-but-ignored-fields)).

---

# 2. AmneziaWG 2.0/3.x (AWG2, AWG3)

> 🧭 How AmneziaWG works, the MTU budget, the graft and validation: [amneziawg-explained](amneziawg-explained.md); typical failures are there too, [§10](amneziawg-explained.md#10-typical-failures). This chapter holds only fields, defaults and errors.

AWG is WireGuard with DPI-evasion obfuscation: AWG2 reshapes the packets, AWG3 also
encrypts their headers and randomises sizes and timings
([§2.10](#210-awg-3x-header-protection-padding-trailers-timings)). AWG is
configured as a normal sing-box `wireguard` endpoint with extra promoted fields.
All of them sit at the endpoint root, none on a peer, as in the `[Interface]`
section of an `awg-quick` `.conf`. With `with_awg` the fields are pushed to the
device. A config without any AWG field is a plain WireGuard endpoint,
byte-identical to upstream behavior.

The runtime is backed by `Leadaxe/wireguard-go` (sagernet/wireguard-go + AmneziaWG
obfuscation, wired via the `submodules/wireguard-go` submodule).

## 2.1 The model: AWG1 vs AWG2 vs AWG3

| Layer | Keys |
|-------|------|
| **AWG1** | `jc`, `jmin`, `jmax`, `s1`, `s2`, `h1`–`h4` as single values |
| **AWG2** | AWG1 plus `i1`–`i5`, `s3`/`s4`, `h1`–`h4` as `"min-max"` ranges, the `id`/`ip`/`ib` sugar |
| **AWG3** | AWG2 plus `header_protection_key`, `content_padding_addition`, `random_trailers`, `disable_cookies`, ranged timings and a ranged `persistent_keepalive_interval` ([§2.10](#210-awg-3x-header-protection-padding-trailers-timings)) |

What each layer does to the packets and why: [amneziawg-explained §0–§4](amneziawg-explained.md#0-awg-adds-three-obfuscation-layers-to-wireguard).

AmneziaWG parameters must match on the client and the server: the junk and
I-packets come from configuration, and the peers do not negotiate them. Set them
from the same `awg.conf` on both ends.

## 2.2 Junk & signature fields

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `jc` | int | `0` (unset) | number of junk packets sent before the handshake |
| `jmin` | int | `0` | minimum size of those junk packets |
| `jmax` | int | `0` | maximum size of those junk packets. Keep it below the real path MTU; the calculation is in [§2.6](#26-mtu-budget) |
| `s1` | int | `0` | junk prepended to the handshake INIT message |
| `s2` | int | `0` | junk prepended to the handshake RESPONSE message |
| `s3` | int | `0` | AWG 2.0 junk-size param; pads only cookie-reply messages and does not affect the MTU |
| `s4` | int | `0` | AWG 2.0 junk-size param; prepended to every transport (data) message, which is why it needs a lower [MTU](#26-mtu-budget) |

## 2.3 Magic headers `h1`–`h4`

`h1`–`h4` override WireGuard's four message-type magic values. Each is either a
single `uint32` or an inclusive `"min-max"` range string.

| Key | Type | Meaning |
|-----|------|---------|
| `h1` | int \| `"min-max"` string | magic for message type 1 (default `1`) |
| `h2` | int \| `"min-max"` string | magic for message type 2 (default `2`) |
| `h3` | int \| `"min-max"` string | magic for message type 3 (default `3`) |
| `h4` | int \| `"min-max"` string | magic for message type 4 (default `4`) |

- A single `uint32` (`1234567890`, AWG 1.x style) fixes the value.
- A range string (`"43613244-384550127"`, AWG 2.0 ranged headers) makes the
  device pick a random value from the range per message.
- `0` or `""` = unset, counted as the WireGuard default `1`/`2`/`3`/`4`. A plain
  number `N` is equivalent to the range `"N-N"`.
- On serialization, a single value marshals back to a JSON number, and only a
  range becomes a JSON string. This keeps the type of the old `uint32` field.

> ⚠️ The four ranges must not overlap; an unset header counts as its WireGuard
> default. Otherwise the device rejects the config with `headers must not overlap`.
> Set all four together, as AWG2 exports do.

## 2.4 CPS decoys `i1`–`i5` and the tag format

`i1`–`i5` are AWG 2.0 Controlled Packet Sequence (CPS) decoy packets, sent in
order before the handshake. They are case-sensitive tag-format strings and map
1:1 to the amneziawg-go `i1`..`i5` keys.

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `i1` … `i5` | string | `""` | CPS decoy packets, sent in order. `i1` typically mimics a real protocol (e.g. a QUIC/STUN header) and is mutually exclusive with the [`id`/`ip`/`ib`](#25-masquerade-sugar-id--ip--ib) sugar |

**Tag format** (lowercase keywords; case and order matter):

| Tag | Emits |
|-----|-------|
| `<b 0xHEX>` | static bytes (the literal hex) |
| `<t>` | a timestamp |
| `<r N>` | N random bytes |
| `<rc N>` | N random characters |
| `<rd N>` | N random digits |

Example: `"i1": "<b 0x000100002112a442><r 12>"` — a static 8-byte prefix followed by
12 random bytes.

## 2.5 Masquerade sugar `id` / `ip` / `ib`

Instead of a hand-written `i1` CPS string, a masquerade can be declared with three
keys: domain, protocol and browser. The key names match
[WireSock Secure Connect](https://www.wiresock.net/). The device generates the `i1`
decoy from them.

| Key | Type | Meaning |
|-----|------|---------|
| `id` | string | Masquerade domain: a host that looks normal for your region, e.g. `www.google.com`. Strict LDH hostname. Embedded into the decoy for `ip=quic` (as the ClientHello SNI), `ip=dns` (as the QNAME) and `ip=sip` (as the host); `ip=stun` has nowhere to carry a hostname and ignores it. Required only for `quic`; for `dns`/`sip` a pseudo name is generated when `id` is absent. A set value is always LDH-validated: values carrying an injection are rejected |
| `ip` | string | Masquerade protocol: `quic` \| `dns` \| `stun` \| `sip` |
| `ib` | string | Masquerade browser: `chrome` \| `firefox` \| `curl`; `chrome-full` is not for client UIs. Only with `ip=quic`. Selects the ClientHello TLS fingerprint and the Initial frame layout (details under `quic` below) |

The decoy is sent before the handshake, like a hand-written `i1`. Each profile is a
packet shaped like its protocol that the client sends first. The shapes come from the
open-source WireSock reference, but instead of a server response the device emits the
client request a peer sends first:

- **`quic`** — a QUIC Initial ([RFC 9001](https://www.rfc-editor.org/rfc/rfc9001)) carrying a whole ClientHello: your `id` as
  the SNI, the fingerprint of the `ib` browser, frames laid out as that stack does:
  - `chrome` — Chrome 155 without the PQ key share, 1250 bytes, the ClientHello cut
    into 2–11 CRYPTO frames with 2–10 PINGs and scattered PADDING
    (`QuicChaosProtector`);
  - `chrome-full` — Chrome 155 with `X25519MLKEM768`, one ~2 KB Initial above the
    MTU (two IP fragments);
  - `firefox` and `curl`/empty — one CRYPTO frame plus PADDING.

  Layout and DCID/random are fresh per call, so there is no cross-user signature.
  `ip=quic` emits one Initial in `i1`. In field runs a ClientHello spread over several
  Initials was dropped on the way to WARP, while frame order inside one packet did not
  matter. A decoy made of a plain QUIC short header did not get through.
- **`dns`** — a client DNS query (QR=0, QTYPE HTTPS/type 65) with your `id` as the
  QNAME and a plain EDNS OPT record with no options. This is the shape a stub resolver
  sends.
- **`stun`** — a WebRTC/ICE STUN Binding Request (magic cookie + USERNAME +
  ICE-CONTROLLING + PRIORITY + SOFTWARE + MESSAGE-INTEGRITY + FINGERPRINT). This is a
  client connectivity-check: an ICE agent legitimately sends it first.
- **`sip`** — a body-less SIP INVITE request (`i1`: request-line + Via /
  Max-Forwards / To / From / Call-ID / CSeq / Contact + `Content-Type:
  application/sdp` and `Content-Length: 0`, no SDP body). The host is your `id` or a
  generated pseudo-host; user names are pronounceable pseudo names. `i2` stays empty:
  the `100 Trying` is the server's reply, not a client packet.

**Where `id` reaches the wire:**

| `ip` | decoy | `id` visible to a middlebox? |
|------|-------|---------------------------|
| `quic` | fragmented QUIC Initial, `id` = SNI | yes: a DPI that decrypts the Initial (keys derive from the DCID) can read `id` if it reassembles the frames in order |
| `dns` | EDNS query (QR=0), `id` = QNAME | yes, in the clear |
| `sip` | SIP INVITE, `id` = host in the URI | yes (when set), in the clear |
| `stun` | STUN Binding Request | no: STUN has no field for a hostname |

Which profile to pick, and why only `quic` passed on a device:
[amneziawg-explained §3.3–§3.4](amneziawg-explained.md#33-masquerade-sugar-id--ip--ib).

### Notes & limitations

- `id`/`ip`/`ib` and an explicit `i1` are mutually exclusive: a config with both is
  rejected.
- `ib` selects the decoy's TLS fingerprint. `chrome`/`firefox` emit a browser
  ClientHello only in builds with TLS-mimicry support; `curl` and an absent `ib` use
  the generic ClientHello. Without that build support the browser profiles fall back
  to the generic one.
- Field status: `ip=quic` is device-proven against LTE DPI. For `dns`/`stun`/`sip`,
  engine acceptance, structural validity and `sing-box check` are confirmed; a field
  A/B was not run.

📖 [Detailed examples](../SPECS/TASKS/009-WIRESOCK_MASQUERADE_PROFILES/EXAMPLES.md) —
full per-profile configs (incl. a Cloudflare WARP one), the generated CPS for each,
and the exact validation errors.

## 2.6 MTU budget

`s4` prepends junk to every transport (data) message, so an AWG endpoint needs a
lower `mtu` than plain WireGuard. `s3` pads only cookie-reply messages and does not
affect the budget. A too-high `mtu` does not stop the handshake but breaks data
transfer:

```
peer(…) - received handshake response
peer(…) - failed to send data packets: write udp4 …: sendmsg: message too long
```

Budget against a 1500-byte path:

```
mtu ≤ 1500 − 28 (UDP/IP) − 32 (WireGuard) − S4 junk bytes
```

For `S4 = 60` that is `mtu ≤ 1380`. AmneziaWG recommends a client MTU of 1280,
leaving headroom for PPPoE and nested tunnels.

**What the core does by itself:**

- `mtu` omitted, `s4` set → default `1280` (plain WireGuard uses `1408`).
- `mtu` set explicitly and over budget → a startup warning. The budget is computed
  against a conservative 1492 (PPPoE): `mtu ≤ 1492 − 28 − 32 − S4`. The tunnel still
  loads.
- The outer socket does not force DF: an oversize outer datagram is IP-fragmented, not
  dropped. Nested tunnels (`masque`/`wireguard`/AWG through `detour`) rely on this.
  `"udp_fragment": false` turns DF on for a specific endpoint. A correct `mtu` is
  still preferred: fragmentation is the safety net.
- Keep `jmax` below the path MTU: a junk packet longer than the MTU gets
  IP-fragmented, and constrained paths drop the fragments.

Why it is this way and what it looks like to the user:
[amneziawg-explained §5](amneziawg-explained.md#5-s4-eats-into-the-mtu-on-every-data-packet).

## 2.7 Mapping an `awg.conf` 1:1

Lines of an `awg.conf` / awg-quick file map to JSON one to one:

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
AWG2 to fit the budget in [§2.6](#26-mtu-budget).

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

The full parameter set of a live AWG 3.1 server, copied 1:1 from its `.conf`.
`H1`–`H4` stay at the WireGuard defaults: with header protection the type word is
masked anyway. The config is verified end-to-end against that server.

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
| `i1` together with `id`/`ip`/`ib` | `amneziawg: id/ip/ib masquerade conflicts with an explicit i1; use one or the other` |
| `id`/`ib` set, `ip` missing | `amneziawg: ip (masquerade protocol) is required when id/ib is set; one of quic\|dns\|stun\|sip` |
| `ip` not in the set | `amneziawg: unknown masquerade protocol "ftp"; one of quic\|dns\|stun\|sip` |
| `ip=quic` without `id` | `amneziawg: id (masquerade domain) is required for ip=quic (it becomes the ClientHello SNI)` |
| `ip=dns` / `ip=sip` / `ip=stun` without `id` | not an error: a pseudo name is generated for `dns`/`sip`; `stun` needs none |
| domain with `\r\n` / `;` / `@` / space | `amneziawg: invalid masquerade domain "...": illegal character (only a-z A-Z 0-9 - _ allowed)` |
| `ib` not in the set | `amneziawg: unknown masquerade browser "safari"; one of chrome\|firefox\|curl` |
| `ib` with `ip≠quic` | `amneziawg: ib (browser) is only meaningful with ip=quic, got ip="dns"` |
| `header_protection_key` with any of `s1`–`s4` below 12 | `amneziawg: s4=8 is too short for header_protection_key: each of s1-s4 must be at least 12 bytes (the padding carries the header cipher nonce)` |
| `header_protection_key` not base64 / not 32 bytes / all zeros | `amneziawg: decode header_protection_key (expected base64, as printed by \`awg genkey\`)` / `… must decode to 32 bytes, got N` / `… is all zeros (the device treats that as "off"); omit the field instead` |
| a ranged field with start > end, or garbage | `invalid range "180-150": range start > end` (the offending key is named: `reject_after_time: …`) |
| `persistent_keepalive_interval` range, built without `with_awg` | `AmneziaWG (awg) support is not included in this build (persistent_keepalive_interval range), rebuild with -tags with_awg` |

The domain passes strict LDH validation, like a WireSock SNI: labels of
`a-z A-Z 0-9 - _`, ≤63 bytes, no leading/trailing hyphen; the whole name ≤253, no
leading dot; one trailing dot allowed. The domain goes into SIP text and a DNS QNAME,
so control bytes and metacharacters are rejected as an injection defense.

## 2.10 AWG 3.x: header protection, padding, trailers, timings

AmneziaWG 3.0 (amneziawg-go v3.0.0, tools v3.0.20260730) and 3.1 (v3.1.2026081x)
add a second layer on top of the AWG2 shape tricks. All of these fields are set on the
endpoint root like the AWG2 fields and need `with_awg`. Amnezia's `amnezia-awg2`
container with `protocol_version: "3.1"` exports this set.

AmneziaWG splits parameters into server-side and client-side. A server-side parameter
must hold the same value on both ends: on a mismatch the handshake silently fails. A
client-side one sets local behaviour, and the server does not care about it. Only
`header_protection_key` is server-side here; the rest are client-side. Copy the
client-side values from the server anyway: they were tuned together.

| Key | Type | Default | Side | Meaning |
|-----|------|---------|------|---------|
| `header_protection_key` | base64 string (32 bytes, `awg genkey`) | `""` (off) | **server** | Encrypts the low-entropy header of every packet: for a handshake message the whole message, for a data packet the 16-byte header (type, receiver index, counter). The cipher is ChaCha20 with this key; the per-datagram nonce is the first 12 bytes of the message's random padding, so each of `s1`–`s4` must be ≥ 12. With it on, the `h1`–`h4` magic values become irrelevant (the type word is random on the wire) and AWG3 exports leave them at `1`–`4`. |
| `content_padding_addition` | int \| `"min-max"` | `""` (off) | client | Extra zero bytes appended to the plaintext of every data packet (inside the AEAD, so random on the wire). The count is picked from the range per packet, instead of WireGuard's pad-to-multiple-of-16. The addition is capped so the datagram never exceeds the largest datagram already seen on this peer's path (the "UDP window", starting at 500 B). A full-MTU packet gets no addition. |
| `random_trailers` | bool | `false` | client | A random tail of random length after every handshake message (init/response/cookie). When no `content_padding_addition` is set, the same tail goes inside data packets under the UDP-window rule. As a result no message kind has a fixed size. The receiver accepts handshake datagrams longer than expected and discards the tail. |
| `disable_cookies` | bool | `false` | client | Never send a cookie reply and skip the under-load mac2 gate that would demand one (a cookie exchange has a recognisable shape). |
| `rekey_after_time` | int \| `"min-max"` seconds | `""` (WireGuard 120) | client | When the initiator starts a fresh handshake. Picked from the range at each check. |
| `rekey_timeout` | int \| `"min-max"` seconds | `""` (WireGuard 5) | client | Spacing between handshake retransmits; jitter is added on top. The range minimum is the floor between two initiations. |
| `reject_after_time` | int \| `"min-max"` seconds | `""` (WireGuard 180) | client | After this a keypair is refused. The range maximum is used wherever the protocol must not expire keys early. |
| `keepalive_timeout` | int \| `"min-max"` seconds | `""` (WireGuard 10) | client | Passive keepalive after received data. |
| `max_handshake_attempts` | int \| `"min-max"` | `""` (WireGuard 18) | client | Retransmits before giving up on a handshake cycle; after that the self-heal rebind runs. Re-picked per cycle. |
| `peers[].persistent_keepalive_interval` | int \| `"min-max"` seconds | `0` (off) | client | The WireGuard number, or an AWG3 range re-picked at every arming. |

Notes:

- The header-protection nonce comes from the padding, so on a plain-size handshake
  (`s1`=0) the key cannot be used. Both the config check
  ([§2.9](#29-validation-errors-verbatim)) and the device refuse such a config.
- `content_padding_addition` and `random_trailers` grow packets, but the MTU budget of
  [§2.6](#26-mtu-budget) stays the same: the UDP-window cap keeps additions inside
  sizes the path already carried. Keep `mtu` at the server-recommended value (`1376`
  in the Amnezia export).
- `random_trailers` widens the receiver's classification: datagrams longer than
  `s1`+148 / `s2`+92 / `s3`+64 are also tried as a handshake by their type word. Our
  receiver is immune to the false matches, while the server's receiver is the
  reference implementation. So do not combine `random_trailers` with wide `h1`–`h4`
  ranges: the uplink would lose packets. Mechanics:
  [amneziawg-explained §6.3](amneziawg-explained.md#63-receive-looks-for-a-live-receiver-index-first).
- The timing overrides don't need to match the server. Inconsistent values, e.g.
  `rekey_after_time` above `reject_after_time`, make the tunnel flap. Copy the
  server's export.
- Everything here is byte-compatible with amneziawg-go v3.1 (commit `b5928ef`,
  2026-08-28): the same nonce/keystream layout, the same classification order, the
  same padding/trailer rules. Verified against a live `protocol_version 3.1` server —
  [SPEC 080](../SPECS/TASKS/080-AWG3_HEADER_PROTECTION_TIMINGS/SPEC.md).

---

# 3. MASQUE outbound (CONNECT-IP / WARP)

> 📖 How MASQUE works and why the defaults are what they are: [masque-explained.md](masque-explained.md). This chapter holds only fields, defaults and errors.

## 3.1 What it is

A `masque` outbound carries whole IP packets over HTTP/3 or HTTP/2 using CONNECT-IP ([RFC 9484](https://www.rfc-editor.org/rfc/rfc9484)),
primarily for Cloudflare WARP. The build needs `with_quic` and `with_gvisor`, both in the default
`LX_TAGS`. The core takes keys and addresses ready from the config: the client registers the
device.

> ⚠️ A top-level `dns` block is required: the outbound resolves domains through the DNS router.
> Without it, domain traffic does not go through the tunnel (`masque: no DNS router available to resolve …`).

> ⚠️ The HTTP version is set by `vhttp`; the `network` key was removed in lx.18
> (`unknown field "network"`). Flat TLS fields are accepted with a warning — see the
> [migration table](#310-migrating-from-the-old-shape).

## 3.2 MASQUE-specific fields

| Key | Type | Required | Default | Meaning |
|-----|------|----------|---------|---------|
| `type` | string | ✅ | — | always `"masque"` |
| `tag` | string | ✅ | — | outbound name (for route/groups) |
| `server` | string | ✅ | — | IP/host of the WARP endpoint |
| `server_port` | uint16 | ✅ | — | port (usually 443) |
| `profile` | string | — | `cloudflare` | `cloudflare` \| `standard` (see [§3.4](#34-profiles--what-profile-switches)) |
| `vhttp` | string | — | `auto` | HTTP version carrying CONNECT-IP: `auto` (h3 first; h2 fallback when the QUIC handshake does not complete in 3 s) \| `h3` (QUIC) \| `h2` (HTTP/2, TCP:443). On `standard` the default quietly means `h3`: there is no h2 leg there. The tcp/udp list is `network_list`, as everywhere else |
| `private_key` | string (base64) | ✅¹ | — | client EC private key, DER (`x509.ParseECPrivateKey`) |
| `public_key` | string (base64) | ✅¹ | — | endpoint PKIX public key, DER (`x509.ParsePKIXPublicKey`, ECDSA) |
| `ip` | string (CIDR) | ✅² | — | local IPv4 inside the tunnel; a bare address → `/32` |
| `ipv6` | string (CIDR) | ✅² | — | local IPv6 inside the tunnel; a bare address → `/128` |
| `tls` | object | — | — | the standard outbound TLS block — `server_name`, `insecure`, `disable_sni`, `fragment`, `record_fragment`, `fragment_fallback_delay`, … Same container every other TLS outbound uses |
| `uri` | string | — | per profile³ | CONNECT-IP request URI |
| `mtu` | int | — | `1280` | userspace-stack MTU. On `h2`, max `16000` (one IP packet = one HTTP/2 DATA frame). Nodes above masque (via `detour`, a group, or on a `chain` position) get a packet size that fits this `mtu` automatically: `mtu` on tunnels, `initial_packet_size` and PMTUD off on hysteria2/tuic/hysteria (at 1280: 1232 for an IPv6 server, 1252 for IPv4; [lx-block.md](lx-block.md#lxdetourmtu_align--path-mtu--packet-size-alignment)). The outer QUIC to the server starts at `mtu + 51` (clamped 1200…1452) |
| `idle_timeout` | duration | — | inherits `lx.masque.idle_timeout`, else off | suspend the tunnel after this long with no traffic (frees the gVisor stack, pumps and QUIC keepalive); the next dial rebuilds it. Absent → the global `lx.masque.idle_timeout` ([lx-block.md](lx-block.md)); an explicit `0` or a negative value keeps this node up whatever the global default; only a positive value enables suspend |
| `keep_alive_period` | duration | — | `30s` | QUIC keepalive (h3 only). A negative value disables it |
| `network_list` | list | — | tcp+udp | L4 protocols routed through the tunnel |

¹ Required for `profile=cloudflare`; optional for `standard`.
² At least one of `ip`/`ipv6` is required.
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

The full list is the shared `DialerOptions` in `option/outbound.go`.

## 3.4 Profiles — what `profile` switches

The profile varies six things; everything else (QUIC/HTTP2, capsule/datagram, the
userspace stack, the pumps) is profile-independent.

| Aspect | `cloudflare` (default) | `standard` ([RFC 9484](https://www.rfc-editor.org/rfc/rfc9484)) |
|--------|------------------------|-----------------------|
| `:protocol` (h3) / `cf-connect-proto` (h2) | `cf-connect-ip` | `connect-ip` |
| Extended CONNECT settings | tolerate absence (WARP does not send it) | require |
| default `tls.server_name` | `www.cloudflare.com` | = `server` |
| default `uri` | `https://cloudflareaccess.com` | none (required) |
| TLS validation | pin `public_key` (ECDSA) | normal chain by `server_name` |
| `private_key`/`public_key` | required | optional |

`standard` targets a self-hosted RFC-compliant CONNECT-IP server and does not
connect to Cloudflare WARP. For WARP use `cloudflare`, the default. `vhttp: h2` is not
implemented for `standard`: such a config fails at load time.

## 3.5 Key material & value formats

**Keys** (`private_key` / `public_key`): base64 of DER. `private_key` =
`x509.MarshalECPrivateKey` (SEC1 EC), `public_key` = `x509.MarshalPKIXPublicKey` of
an `*ecdsa.PublicKey` (P-256). The WARP registration produces keys in this format
on the client side, and the core parses them without conversion.

**`ip` / `ipv6`**: an address or a CIDR. `"172.16.0.2"` → `172.16.0.2/32`,
`"2606:…::"` → `/128`. These are the local addresses inside the tunnel (your
address in the WARP network), not the exit IP.

**Duration** (`idle_timeout`, `keep_alive_period`, `connect_timeout`, …): a Go
duration string — `"30s"`, `"5m"`, `"1h30m"`. Empty = default. Negative (`"-1s"`) =
"disable".

## 3.6 SNI strategy

The default SNI is `www.cloudflare.com`, not the endpoint name; the endpoint is checked against
the pinned `public_key`. `tls.server_name` can be overridden per node. `tls.disable_sni: true`
sends no SNI at all. Why — [masque-explained §2.1](masque-explained.md#21-the-sni-is-a-neutral-popular-host).

## 3.7 `vhttp`: auto / h3 / h2

- `auto` (default) — `h3` first; if the QUIC handshake does not complete within 3 s, `h2`. The
  winning version is remembered ([masque-explained §3](masque-explained.md#3-http3-or-http2)).
- `h3` — QUIC only.
- `h2` — HTTP/2 over TCP:443 only; one IP packet = one DATA frame, `mtu` up to 16000. On `h2` the
  ClientHello is guarded by the node's `fragment` / `record_fragment` and the `tls` section of
  `lx.sniff_guard`, on `h3` by the `quic` section
  ([lx-block](lx-block.md#lxsniff_guard--guard-against-rerouting-by-sni)).

## 3.8 Idle-suspend & keepalive

- `idle_timeout` — idle time without traffic after which the tunnel closes; the next dial
  rebuilds it. Off by default: absent, `0` and negative keep the tunnel up; only a positive value
  (e.g. `"5m"`) enables it.
- `keep_alive_period` (default `30s`, h3 only) — the QUIC keepalive interval; a negative value
  disables it.

The cost of sleep and how it interacts with keepalive are in
[masque-explained §5](masque-explained.md#5-idling-and-waking).

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
- `profile=standard` without `uri` → `masque: uri is required for the standard profile — set it to the server's CONNECT-IP request URI, e.g. https://<host>/.well-known/masque/ip/*/*/` (checked before `vhttp`, so the text is the same for h2/h3/auto);
- `public_key` is not an ECDSA key → `public_key is not an ECDSA key`.

## 3.10 Migrating from the old shape

The flat TLS fields in the table below are accepted; each one logs one deprecation
line per outbound. A legacy field that disagrees with its replacement is a hard
error: the core does not silently pick one of the two.

The legacy `network` key for the HTTP version was removed in `v1.14.3-lx.18`: a
masque outbound carrying it fails to decode with `unknown field "network"`. Rename the
key to `vhttp`; the values are the same.

| Legacy | Current |
|---|---|
| `network: "h3"` / `"h2"` — removed | `vhttp: "h3"` / `"h2"` |
| `sni` | `tls.server_name` |
| `skip_cert_verify: true` | `tls.insecure: true` |
| `fragment: true` | `tls.fragment: true` |
| `fragment_fallback_delay` | `tls.fragment_fallback_delay` |
| `record_fragment: true` | `tls.record_fragment: true` |

> ⚠️ The legacy booleans cannot tell "absent" from an explicit `false`, so only a
> legacy `true` carries over. To turn something off, write the `tls` form.

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
   tcp/udp list is `network_list`. `"vhttp": "tcp"` is a fail-fast error. On every
   other outbound the tcp/udp list is called `network`.
2. **Top-level `dns` block is required.** Without it, domain traffic through the
   tunnel does not resolve.
3. **The exit IP changes** after idle-suspend/reconnect: WARP anycast hands out
   different edge addresses per new connection. The internal `ip`/`ipv6` stays
   stable.
4. **`tls.insecure: true`** removes public-key pinning entirely. When WARP SNI is
   masqueraded, pinning is the only defense left, so `insecure` is for debugging only.
5. **`keep_alive_period` vs `idle_timeout`.** How they differ:
   [§3.8](#38-idle-suspend--keepalive).

**📖 Status.** Verified end-to-end on a device over Wi-Fi and LTE: `warp=on`, traffic
flows over both `h3` and `h2`, idle-suspend and the self-healing reconnect work.

---

# 4. gRPC transport

> 🧭 An upstream transport with no explanatory section; only the `service_name` forms where our behaviour differs from upstream are listed here.

The gRPC transport (`"transport": { "type": "grpc" }`) needs no build tag: our builds
ship the lite implementation (`v2raygrpclite`). The full `grpc-go` one is built under
`with_grpc` and is not in the desktop/CLI binary.

## 4.1 `service_name`: the Xray forms

`service_name` has two forms, as in Xray. A single character picks the form: a
leading `/`.

- **No leading `/`** — the whole value is one service segment. It is escaped as a
  whole (`/` inside it becomes `%2F`) and the stream name is always `Tun`. This
  form matches upstream byte for byte.
- **Leading `/`** — the value is a custom path in Xray's convention. Every segment
  is escaped separately, so `/` stays a path separator. The last segment is the
  stream name; a `|…` tail on the stream name (Xray's multi-stream name) is dropped.

| `service_name` | Path on the wire | Service / stream | Note |
|---|---|---|---|
| `TunService` | `/TunService/Tun` | `TunService` / `Tun` | no leading `/` |
| `a/b` | `/a%2Fb/Tun` | `a%2Fb` / `Tun` | no leading `/`, = Xray's old form |
| `/a/b/Tun` | `/a/b/Tun` | `a/b` / `Tun` | custom path, = Xray |
| `/a/b/Stream` | `/a/b/Stream` | `a/b` / `Stream` | stream name comes from the config |
| `/a b/Tun` | `/a%20b/Tun` | `a%20b` / `Tun` | per-segment `PathEscape` |
| `/a/b/Tun\|TunMulti` | `/a/b/Tun` | `a/b` / `Tun` | multi tail dropped, as Xray does |

There is no new option and no validation: the form is chosen by the content of
`service_name`, the way Xray does it.

**Notes.**

1. The `with_grpc` client does not escape the path for the no-leading-`/` form: it
   sends `"/" + name + "/Tun"` raw, so `a/b` goes out as `/a/b/Tun`. That is
   upstream behaviour and we did not change it. The difference only shows up for
   such a name that itself contains `/`.
2. Our lite server compares the decoded path, so it is more lenient than Xray: a
   server configured with `service_name: "a/b"` accepts both `/a%2Fb/Tun` and
   `/a/b/Tun`. An Xray server compares strictly, so pick the same form on both ends.
3. **Symptom of a form mismatch** against an Xray server:
   `v2ray-grpc: unexpected status: 404 Not Found`. The dial reached the server, and
   the server rejected it on the path.

**📖 Status.** Implemented and covered by unit and client↔server tests. Not yet run
against a live Xray: the wire path was checked against the Xray and grpc-go source.
Available since `v1.14.1-lx.8`.

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
| `key_share` | `""` \| `"hybrid"` \| `"classical"` | `""` | Key share form in the ClientHello ([§5.2](#52-fingerprints-and-the-hybrid-key-share)) |

| `key_share` | ClientHello | Works against |
|---|---|---|
| `""` | whatever the fingerprint carries ([§5.2](#52-fingerprints-and-the-hybrid-key-share)) | depends on the fingerprint ([§5.2](#52-fingerprints-and-the-hybrid-key-share)) |
| `"classical"` | `X25519MLKEM768` removed from `key_share` and `supported_groups`; `chrome`: 594 bytes instead of 1720, one TCP segment instead of two | Xray < v26.9.8 only; newer servers reject silently (`reality verification failed`) |
| `"hybrid"` | the hybrid is required; on a fingerprint without it the handshake fails with a message ([§5.4](#54-reality-validation-errors-verbatim)) | as `""` |

There is no automatic fallback between `hybrid` and `classical`. The Xray fields
`spiderX`, `mldsa65Verify` and `realitySettings.password` are not accepted by the core.

## 5.2 Fingerprints and the hybrid key share

`tls.utls.fingerprint` is an upstream key (enum: `chrome`, `chrome_psk`,
`chrome_psk_shuffle`, `chrome_padding_psk_shuffle`, `chrome_pq`, `chrome_pq_psk`,
`firefox`, `edge`, `safari`, `360`, `qq`, `ios`, `android`, `random`, `randomized`;
plus our `chrome_155`). Under REALITY the fingerprint decides whether the server lets
the node in: Xray ≥ v26.9.8 accepts only a ClientHello with the `X25519MLKEM768` hybrid
key share before `X25519`. The core does not substitute the fingerprint.

| `fingerprint` | Key share in the ClientHello | Xray ≥ v26.9.8 | Preset source |
|---|---|---|---|
| `chrome` and the upstream `chrome_*` | GREASE, **X25519MLKEM768**, X25519 | passes | metacubex/utls, Chrome 133 |
| `chrome_155` | GREASE, **X25519MLKEM768**, X25519 | passes | utls-lx fork, Chrome 155: opt-in, adds ML-DSA signature schemes and `trust_anchors`; `chrome` stays 133 |
| `firefox` | **X25519MLKEM768**, X25519 | passes | utls-lx fork, Firefox 148 |
| `safari` | **X25519MLKEM768**, X25519 | passes | utls-lx fork, Safari 26.3 |
| `edge`, `ios`, `android`, `360`, `qq` | X25519 only | rejected | no presets with the hybrid exist |
| `random` | as the drawn preset: `chrome`, `firefox` or `safari` | passes | pool without `edge`/`ios` |
| `randomized` | **X25519MLKEM768**, X25519, P-256; ALPN always | passes | `HelloRandomizedALPN` with the hybrid always on; the rest stays random |

`AuthKey` is computed from the key the server will choose: pure X25519 if present in
`key_share`, otherwise the X25519 part of the hybrid. The client version in the session
id is the required minimum.

The choice is per node because the core cannot choose between "the server rejects" and "the
network loses": some networks lose the two-segment hybrid hello while the one-segment classical
hello passes (LxBox #142), Xray's own client hits the same (XTLS#6256), and new Xray servers
require the hybrid. `classical` lowers post-quantum protection and fits old servers only.

## 5.3 ClientHello fragmentation

Upstream `tls` keys; in the fork they also apply to REALITY.

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `fragment` | bool | `false` | Cut the first packet by SNI into TCP segments, waiting for the ACK ([upstream](../docs/configuration/shared/tls.md#fragment)) |
| `record_fragment` | bool | `false` | The same cut points, each its own TLS record, one packet ([upstream](../docs/configuration/shared/tls.md#record_fragment)) |
| `fragment_fallback_delay` | duration | `500ms` | The pause used when the ACK wait time cannot be computed ([upstream](../docs/configuration/shared/tls.md#fragment_fallback_delay)) |

There is no default under `detour`: without the `lx.sniff_guard` block the ClientHello is
split only where the node sets `fragment` or `record_fragment`, through a `detour` too.
The `lx.sniff_guard` block configures protection against relays that reroute by SNI
([lx-block](lx-block.md#lxsniff_guard--guard-against-rerouting-by-sni)). A node's
explicit value, `"record_fragment": false` included, wins over the block unless the block
sets `"override": true`. Only the handshake is fragmented. How the ClientHello is split
under `detour` overall:
[lx-config §9](lx-config.md#9-clienthello-under-detour--no-default).

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
| `encryption` | string | `""` | `""` / `"none"`: layer off, upstream behaviour byte for byte. Otherwise a spec string validated at `check`/start. In Xray this is `users[0].encryption`, carried over verbatim |

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

Verified on a device on the subscription that prompted the field (`v1.14.0-lx.18`): 10 nodes that
were dead without it came up (6 of 8 WS, 4 of 4 gRPC), and nothing changed in the other transport
groups. In the field the `native.0rtt` form is seen; `1rtt` with padding and `xorpub`/`random`
parse and build, but no live server with them has been met yet.

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

# 6. TrustTunnel outbound and inbound

> Built into the desktop and router release binaries since lx.21: the `with_trusttunnel`
> tag is in `LX_TAGS`. HTTP/3 also needs `with_quic`, which is in the set too. The
> Android AAR does not carry the tag. A custom build without it rejects both types at load
> time: `trusttunnel is not included in this build, rebuild with -tags with_trusttunnel`.

## 6.1 What it is

TrustTunnel is the AdGuard VPN protocol, licensed under Apache-2.0. The wire specification
is [`PROTOCOL.md` v1.0](https://github.com/TrustTunnel/TrustTunnel/blob/master/PROTOCOL.md)
in the official repository `TrustTunnel/TrustTunnel`. The protocol runs HTTP/2 over TLS or
HTTP/3 over QUIC to a real domain, with a proxy `CONNECT` and Basic auth inside.
Every TCP connection is a `CONNECT host:port` stream, every UDP session a
`CONNECT _udp2` stream carrying framed datagrams; all streams of a node share one
session. The `trusttunnel` outbound talks to the official endpoint
(`trusttunnel_endpoint`) and to our inbound; the `trusttunnel` inbound serves the
official clients and our outbound. Feature: [018-TRUSTTUNNEL](../SPECS/FEATURES/018-TRUSTTUNNEL/FEATURE.md).

## 6.2 Outbound fields

| Field | Type | Default | Meaning |
|-------|------|---------|---------|
| `server`, `server_port` | string, number | required | Endpoint address. An IP literal requires `tls.server_name` (§6.3) |
| `username`, `password` | string | required | Sent as `proxy-authorization: Basic` (standard base64 with padding) on every `CONNECT` |
| `network` | `tcp` \| `udp` \| list | both | Networks the node carries |
| `quic` | bool | `false` | `false` — HTTP/2 over TLS/TCP, `true` — HTTP/3 over QUIC (needs `with_quic`) |
| `quic_congestion_control` | `bbr` \| `cubic` \| `reno` | `bbr` | Ignored without `quic` |
| `health_check` | bool | `false` | `CONNECT _check` before a dial on a session silent for more than 30 s (deadline 7 s); never on a timer |
| `idle_timeout` | duration | `lx.trusttunnel.idle_timeout`, else `5m` | Close the session locally after this long without activity and without open streams; `"0"` disables teardown |
| `tls` | sing-box `tls` block | required, `enabled: true` | `server_name`, `certificate`, `insecure`, `utls`, `fragment`, `ech`. ALPN: empty → `h2` (`h3` with `quic`) |
| Dial Fields | — | — | `detour`, `bind_interface`, `domain_resolver`, … |

The global default is set in the root `lx` block: absent or `0` means 5 minutes. A node's
own `idle_timeout` wins, including an explicit `"0"`:

```json
{ "lx": { "trusttunnel": { "idle_timeout": "10m" } } }
```

For HTTP/2, uTLS with the Chrome fingerprint is recommended:

```json
"tls": { "enabled": true, "server_name": "vpn.example.com", "utls": { "enabled": true, "fingerprint": "chrome" } }
```

With `quic: true` uTLS is unavailable: start fails with `uTLS is not supported with quic`.
HTTP/3 keeps quic-go's fingerprint: its QUIC Initial, ClientHello and HTTP/3 SETTINGS are
Go's. On HTTP/2 the session's SETTINGS values and connection window are Chrome's
(`HEADER_TABLE_SIZE 65536`, `ENABLE_PUSH 0`, `INITIAL_WINDOW_SIZE 6291456`,
`MAX_HEADER_LIST_SIZE 262144`, `WINDOW_UPDATE 15663105`), with or without uTLS. The
SETTINGS order, an extra `MAX_FRAME_SIZE`, the pseudo-header order and HEADERS without
priority stay Go's, because `x/net/http2` is used without a fork. The protocol has no
padding, so TLS-in-TLS of a proxied HTTPS connection stays visible to traffic analysis.

## 6.3 Start-time validation (verbatim)

| Condition | Error |
|-----------|-------|
| `tls` missing or `enabled: false` | `TLS required` |
| `server` is an IP, `tls.server_name` empty | `tls.server_name is required when server is an IP address: the server routes by SNI and drops a ClientHello without it` |
| empty `username` / `password` | `missing username` / `missing password` |
| `icmp` in `network` | `outbounds[N].network: unknown network: icmp` (config decoding) |
| `quic: true` + `tls.utls.enabled` | `uTLS is not supported with quic` |
| unknown congestion control | `unknown quic_congestion_control: <value>` |
| `tls.alpn` set without `h2` (`h3` with `quic`) | `tls.alpn must contain h2` |
| `quic: true` in a build without `with_quic` | `trusttunnel: HTTP/3 requires the with_quic build tag` |
| `lx.trusttunnel.idle_timeout` < 0 | `lx.trusttunnel.idle_timeout must be >= 0` |

## 6.4 Session behaviour

- **The dial waits for the server's answer.** 200 → a connection; anything else is an
  error from the dial with the code and `X-Warning`, e.g.
  `trusttunnel: server responded 502 Bad Gateway: 300 - Connection failed for some reason`
  or `trusttunnel: server responded 407 Proxy Authentication Required`. A 403/404/405/407
  also tears the session down.
- **No keepalive, no timers while idle.** No HTTP/2 PING, no QUIC keepalive. The HTTP/3
  session's QUIC idle timeout is a fixed 600 s, the endpoint's own
  `client_listener_timeout_secs`; the idle window below applies on top of it. If a session has been silent for longer than
  the idle window, it is treated as stale: the next `CONNECT` on it gets 3 s, then the session is rebuilt and the
  `CONNECT` repeated once within the caller's deadline.
- **One session per node.** At the endpoint's stream limit (1000 on HTTP/2, 4096 on
  HTTP/3) a dial waits for a free stream within its deadline; no second session is opened.
- **Network change** (`InterfaceUpdated`) drops the session; QUIC migration is off on
  the endpoint.
- **DNS asymmetry.** TCP domains go to the endpoint unresolved and are resolved there. UDP
  frames carry IP addresses only, so a UDP destination domain is resolved locally with
  the node's `domain_resolver` (else `route.default_domain_resolver`, else the DNS rules).
  For UDP DNS that does not leak, detour the resolver through the node itself.
- **IPv6 inside `::/96`** (including `::1`) cannot be carried by a UDP frame: 12 zero bytes
  mean IPv4 in the frame format, and the official endpoint decodes them the same way.
- The node identifies itself as `user-agent: <platform> sing-box-lx` (`Mac`, `Windows`,
  `Linux`, `Android`, `iOS`) and sends `sing-box-lx` as the application name in every UDP
  frame.

## 6.5 Outbound key mapping

The table puts three sources side by side:

- the official CLI client `trusttunnel_client` 1.1.11 — the `[endpoint]` table of its
  TOML; the endpoint's export, subscription and `tt://` link carry the same keys;
- the fork sing-box-extended 2.7.2 — keys of its
  [`examples/trusttunnel/client.json`](https://github.com/shtorm-7/sing-box-extended/tree/v1.14.1-extended-2.7.2/examples/trusttunnel);
- this fork.

sing-box rejects unknown keys at load time, so an extended config needs the renames below
before it starts here.

| Official client (TOML) | sing-box-extended 2.7.2 | sing-box-lx | Note |
|------------------------|-------------------------|-------------|------|
| `addresses = ["ip:port"]` | `server`, `server_port` | `server`, `server_port` | One node per address. An IP in `server` needs `tls.server_name` |
| `hostname` (`custom_sni` if set) | `tls.server_name` | `tls.server_name` | The endpoint routes by SNI |
| `username`, `password` | `username`, `password` | `username`, `password` | An empty `password` is a start error here |
| — | `network` | `network` | `tcp`, `udp` or both (default) |
| `upstream_protocol = "http2"` / `"http3"` | `quic: false` / `true` | `quic: false` / `true` | The client's `auto` (H2/H3 race) is a `urltest` group of two nodes here |
| — | `congestion_controller` (`bbr`, `bbr_standard`, `bbr2`, `bbr2_variant`, `cubic`, `reno`) | `quic_congestion_control` (`bbr`, `cubic`, `reno`) | Named as on the upstream `naive` outbound; rename the key |
| — | `cwnd` | — | Not supported; drop the key |
| — | `multiplex` (`max_connections`, `min_streams`, `max_streams`) | — | One session per node (§6.4); drop the key |
| — (the client checks on events itself) | `health_check` | `health_check` | Here: `CONNECT _check` on events only, never on a timer |
| — | — | `idle_timeout` (default `5m`, `lx.trusttunnel.idle_timeout`) | This fork only |
| `tls_profile` (`chrome`, `safari`, `firefox`, …) | `tls.utls.fingerprint` | `tls.utls.fingerprint` | HTTP/2 only; recommended `chrome` (§6.2); with `quic: true` a start error |
| `certificate` (PEM) | `tls.certificate` / `certificate_path` | `tls.certificate` / `certificate_path` | The `tt://` link carries a DER chain; the importer converts it to PEM |
| `skip_verification` | `tls.insecure` | `tls.insecure` | |
| `anti_dpi` | `tls.fragment` | `tls.fragment` | The closest built-in equivalent, not the same split |
| `has_ipv6 = false` | `domain_resolver.strategy: "ipv4_only"` | `domain_resolver.strategy: "ipv4_only"` | The endpoint refuses IPv6 destinations |
| `client_random` (export: `client_random_prefix`), `client_random_auth_key` | — | — | Not supported: a server with such rules drops the ClientHello |
| `dns_upstreams`, `vpn_mode`, `exclusions`, `killswitch_*`, `post_quantum_group_enabled`, `[listener.*]`, `name` | `dns`, `route`, `inbounds` | `dns`, `route`, `inbounds` | Not node settings: these are sing-box's DNS, routing and local inbounds |

## 6.6 Inbound fields

| Field | Type | Default | Meaning |
|-------|------|---------|---------|
| Listen Fields | — | — | `listen`, `listen_port`, … |
| `users` | `[{username, password}]` | required | Empty list or an empty field in an entry — start error (`missing users`, `users[N]: missing password`) |
| `network` | `tcp` \| `udp` \| list | both | Which listeners to open, as on the upstream `naive` inbound: `tcp` — HTTP/2 over TLS, `udp` — HTTP/3 over QUIC. `udp` without `with_quic`: start error when alone, a warning (`HTTP/3 disabled: …`) when both. Tunnelled networks are always TCP and UDP; restrict them with route rules |
| `quic_congestion_control` | `bbr` \| `cubic` \| `reno` | `bbr` | Unknown — start error |
| `tls` | sing-box inbound `tls` block | required, `enabled: true` | ALPN is set by the inbound: `h2` is prepended on TCP, `h3` appended on UDP |

Each TCP `CONNECT` reaches the router with `inbound`, `user`, `source` (the client),
`destination` (a domain stays a domain) and `origin_destination` (the listener address).
Each UDP flow of a `_udp2` stream reaches it as a packet connection with the frame's
source. When the client sends a zero source, the session address becomes the source.

## 6.7 Inbound behaviour

- **The answer to `CONNECT` follows the dial.** 200 goes out on the router's first read of
  the connection, a failed dial is 502 + `X-Warning` (`300 - Connection failed for some
  reason`, `301 - …` for an unreachable host, `302 - …` for a timeout), the same way the
  official endpoint answers. With a `sniff` route action the router reads before dialing,
  so the 200 goes out early and a later dial failure is a plain stream close.
- **Refused credentials** → 407 + `proxy-authenticate: Basic realm=Authorization Required`;
  the log line names the source address, never the credentials.
- **Not the AdGuard product.** No rules by `client_random`, no subscription endpoint, no
  ping / speed-test hosts, no reverse proxy, no ICMP (`CONNECT _icmp` → 404). Every
  non-tunnel request (a browser, a scanner) gets 404 with an empty body and no
  `proxy-authenticate`. HTTP/1.1 is not served.
- **`allow_private_network_connections = false` of the endpoint** is a route rule here:

```json
{ "route": { "rules": [ { "inbound": "tt-in", "ip_is_private": true, "action": "reject" } ] } }
```

## 6.8 Inbound key mapping

The official endpoint (`trusttunnel_endpoint` 1.1.0: `vpn.toml`, `credentials.toml`,
`hosts.toml`), sing-box-extended 2.7.2 (keys of its
[`examples/trusttunnel/server.json`](https://github.com/shtorm-7/sing-box-extended/tree/v1.14.1-extended-2.7.2/examples/trusttunnel))
and this fork.

| Official endpoint (TOML) | sing-box-extended 2.7.2 | sing-box-lx | Note |
|--------------------------|-------------------------|-------------|------|
| `listen_address = "0.0.0.0:443"` | `listen`, `listen_port` | `listen`, `listen_port` | |
| `[listen_protocols.http2]` / `[listen_protocols.quic]` present | `network: tcp` / `udp` | `network: tcp` / `udp` | Which listeners open; both by default. Tunnelled networks are always TCP and UDP |
| `credentials.toml` `[[client]]` `username`, `password` | `users[].name`, `users[].password` | `users[].username`, `users[].password` | `username`, as on every upstream inbound; rename `name` |
| `hosts.toml` `[[main_hosts]]` `hostname`, `cert_chain_path`, `private_key_path` | `tls.server_name`, `certificate_path`, `key_path` | `tls.server_name`, `certificate_path`, `key_path` | |
| — | `tls.alpn: ["h2", "h3"]` | set by the inbound | `h2` on TCP, `h3` on UDP; an explicit list is kept and completed |
| — | `congestion_controller` | `quic_congestion_control` (`bbr`, `cubic`, `reno`) | Rename the key |
| — | `cwnd` | — | Not supported; drop the key |
| `client_listener_timeout_secs` | — | fixed 600 s | Idle session limit, as the endpoint's default |
| `max_concurrent_streams`, `initial_max_streams_bidi` | — | fixed 1000 (H2) / 4096 (H3) | The endpoint's defaults |
| `allow_private_network_connections = false` | — | route rule `ip_is_private` + `action: reject` | Example in §6.7 |
| `auth_failure_status_code` | — | fixed: 407 on `CONNECT`, 404 otherwise | |
| `ipv6_available = false` | — | route rules / DNS `strategy` | |
| `udp_connections_timeout_secs` | — | sing-box's UDP timeout | `udp_timeout` of a `route-options` rule, 5 min by default |
| `rules_file` (`client_random` rules), `[reverse_proxy]`, `[icmp]`, `[metrics]`, `[subscription]`, ping / speed-test hosts | — | — | Not supported |

## 6.9 Examples

### Outbound

```json
{
  "type": "trusttunnel",
  "tag": "tt-node",
  "server": "vpn.example.com",
  "server_port": 443,
  "username": "user",
  "password": "secret",
  "network": "",
  "quic": false,
  "quic_congestion_control": "bbr",
  "health_check": false,
  "idle_timeout": "5m",
  "tls": {
    "enabled": true,
    "server_name": "vpn.example.com",
    "utls": { "enabled": true, "fingerprint": "chrome" }
  }
}
```

### Inbound

```json
{
  "type": "trusttunnel",
  "tag": "tt-in",
  "listen": "::",
  "listen_port": 443,
  "users": [
    { "username": "user", "password": "secret" }
  ],
  "network": "",
  "quic_congestion_control": "bbr",
  "tls": {
    "enabled": true,
    "server_name": "vpn.example.com",
    "certificate_path": "/etc/sing-box/fullchain.pem",
    "key_path": "/etc/sing-box/key.pem"
  }
}
```

A loopback stand with the official endpoint and client lives in
[`lx-test/trusttunnel/`](../lx-test/trusttunnel/README.md). The official client connects
loopback destinations directly, bypassing the tunnel, so `check-in` reaches the origin by the
name `localhost` (`curl --socks5-hostname`): the domain goes into the `CONNECT` and is resolved
by our inbound.

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
  [VLESS_ENCRYPTION](../SPECS/FEATURES/012-VLESS_ENCRYPTION/),
  [TRUSTTUNNEL](../SPECS/FEATURES/018-TRUSTTUNNEL/).
