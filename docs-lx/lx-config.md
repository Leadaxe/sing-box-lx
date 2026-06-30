# sing-box-lx — configuration of the downstream features

`sing-box-lx` is upstream [sing-box](https://github.com/SagerNet/sing-box) plus a small set of **client-side** features (currently two), each gated behind a build tag:

| Feature | Build tag | Where it lives in config |
|---------|-----------|--------------------------|
| **XHTTP** transport (Xray-compatible) | `with_xhttp` | `transport.type: "xhttp"` on a VLESS / VMess / Trojan outbound |
| **AmneziaWG 2.0** (AWG2) | `with_awg` | extra fields on a `wireguard` **endpoint** |

Build the binary with both: `make -f Makefile.lx lx-build` (output `sing-box`, version `…-lx.N`).
Without a tag the feature is absent: an `xhttp` transport or an AWG field is rejected at load time with an explicit error (no silent downgrade).

> ⚠️ All keys/UUIDs below are **placeholders**. Never commit real private keys / pre-shared keys to a repository.

---

## 0. Every field at a glance (exhaustive example)

One config carrying **every** field sing-box-lx adds on top of upstream — XHTTP transport,
AmneziaWG 2.0 endpoint, the `id`/`ip`/`ib` masquerade sugar, and the `urltest` `round_robin`
balancer. This is a **kitchen-sink reference**, not a recommended config: many fields are
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
        "no_grpc_header": false,                // default: false. Omit Xray gRPC-style headers (forward-compat)

        // ── session / seq placement (v2) ──
        "session_placement": "path",            // default: path. path | query | header | cookie
        "session_key": "",                      // default: X-Session (header) / x_session (query|cookie); unused for path
        "seq_placement": "path",                // default: path. path | query | header | cookie (packet-up)
        "seq_key": "",                          // default: X-Seq (header) / x_seq (query|cookie); unused for path

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
      "mtu": 1280,                              // lower than plain WG (s3/s4 junk); core defaults 1280 if s3/s4 set
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

> **Field count:** 26 XHTTP + 21 AmneziaWG (incl. `id`/`ip`/`ib`) + 5 `urltest` (`mode` +
> `balancer{pool,pool_tolerance,sticky_hash}`). Mutually-exclusive / ignored fields are
> labelled inline above; the sections below give the per-field semantics, gotchas and live
> verification status.

---

## 1. XHTTP transport

XHTTP (Xray "splithttp"/"xhttp") is a v2ray transport that tunnels the proxy over plain HTTP/2 requests. It attaches to VLESS / VMess / Trojan via the shared `transport` block and composes with TLS, including **Reality**. (XHTTP is incompatible with XTLS-Vision — that is a protocol limitation, not ours.)

### Fields (`transport`)

The default wire shape (everything below at its default) is **byte-identical to the
live-verified v1 client**, so existing configs are unaffected — the v2 fields are all opt-in.

**Core (v1):**

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `type` | string | — | must be `"xhttp"` |
| `mode` | string | `auto` | `auto` \| `packet-up` \| `stream-up` \| `stream-one`. **`auto` resolves to `stream-one` on a Reality TLS, else `packet-up`** (both live-validated). `stream-one` had a downlink-framing bug, fixed in task 011 and live-verified on Reality nodes; select it explicitly only if you know the server needs it. |
| `host` | string | TLS SNI / server | overrides the HTTP `Host` header |
| `path` | string | `""` (root) | request path prefix; the session id (and, for `packet-up`, the upload sequence number) are appended as path segments when their placement is `path` |
| `headers` | object | — | extra request headers sent on every XHTTP request |
| `x_padding_bytes` | string | `"100-1000"` | inclusive byte-length **range** of the padding value (`"min-max"` or a single int). Drives both the legacy Referer `x_padding` length and the obfs-mode padding length |
| `no_grpc_header` | bool | `false` | omit Xray's gRPC-style headers in some modes (forward-compat) |

**Session / seq placement (v2)** — where the per-request session id and (packet-up) upload sequence number are carried:

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `session_placement` | string | `path` | `path` \| `query` \| `header` \| `cookie` |
| `session_key` | string | `X-Session` (header) / `x_session` (query\|cookie) | name carrying the session id when placement ≠ `path`; unused for `path` |
| `seq_placement` | string | `path` | `path` \| `query` \| `header` \| `cookie`. For `path`, the seq is the **second** appended segment (session id first — order is load-bearing) |
| `seq_key` | string | `X-Seq` (header) / `x_seq` (query\|cookie) | name carrying the seq when placement ≠ `path`; unused for `path` |

**Uplink-data placement (v2, packet-up)** — where the upload payload goes:

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `uplink_data_placement` | string | `auto` | `body` \| `auto` (== body) \| `header` \| `cookie`. `header`/`cookie` are **only valid in `packet-up`** (else error); they carry the payload as `base64.RawURLEncoding`, chunked into `<key>-<i>` headers / `<key>_<i>` cookies |
| `uplink_data_key` | string | `X-Data` (header) / `x_data` (cookie) | base name for the chunked header/cookie payload; `""` for body |
| `uplink_chunk_size` | string | cookie `2048-3072`, header `3000-4000`, else `= sc_max_each_post_bytes` | `"min-max"` range (in base64 chars) of each chunk; min floored to 64 |
| `uplink_http_method` | string | `POST` | HTTP method for **upload** requests (download is always GET); upper-cased; `GET` allowed only in `packet-up` |

**X-Padding obfuscation (v2)** — only active when `x_padding_obfs_mode` is `true`; otherwise the legacy Referer padding (note below) is used:

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `x_padding_obfs_mode` | bool | `false` | master switch. `false` → legacy Referer `x_padding`. `true` → the configurable `x_padding_*` family below |
| `x_padding_placement` | string | `queryInHeader` | `cookie` \| `header` \| `query` \| `queryInHeader` |
| `x_padding_key` | string | `x_padding` | cookie/query param name (unused for `header` placement) |
| `x_padding_header` | string | `X-Padding` | header name (for `header` / `queryInHeader` placement) |
| `x_padding_method` | string | `repeat-x` | `repeat-x` (N literal `X` bytes) \| `tokenish` (base62 token whose HPACK-Huffman length is tuned to ~N) |

**Packet-up tuning (v2):**

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `sc_max_each_post_bytes` | string | `"1000000-1000000"` | `"min-max"` range bounding a single upload POST (the split threshold) |
| `sc_min_posts_interval_ms` | string | `"30-30"` | `"min-max"` anti-burst delay between successive POSTs, in ms |

**Accepted but ignored by the client** (present so an inbound-shaped config or a symmetric link doesn't error — the client never acts on them): `sc_max_concurrent_posts`, `server_max_header_bytes`, `no_sse_header`, `sc_max_buffered_posts`, `sc_stream_up_server_secs`.

> **Note (default wire format):** with `x_padding_obfs_mode` off (the default), padding is carried as `x_padding=<zeros>` inside the `Referer` header (Xray's default placement) — live-validated against a real Xray (3x-ui) server. The server validates the `x_padding` length (default 100–1000) and replies `400` without it. Client and server Xray versions should still match (XHTTP evolves quickly).

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

AWG2 = AWG1 fields **plus** the CPS packets `I1`–`I5`. Both client and server must run AmneziaWG with **matching** parameters (the I-packets are configuration, not negotiated). For a friendlier way to set the first decoy, see the WireSock-style [`id`/`ip`/`ib`](#masquerade-id--ip--ib-wiresock-style-sugar-over-i1) sugar below, which generates `i1` for you.

### Fields (on the `wireguard` endpoint, alongside `private_key`/`peers`/…)

| Key | Type | Meaning |
|-----|------|---------|
| `jc` | int | default `0` (unset). Number of junk packets sent before the handshake |
| `jmin` / `jmax` | int | default `0`. Min / max size of those junk packets |
| `s1` / `s2` | int | default `0`. Junk prepended to the handshake **INIT** / **RESPONSE** messages |
| `s3` / `s4` | int | default `0`. AWG 2.0 junk-size params (companions to `s1`/`s2`). Their overhead is what drives the [lower MTU](#mtu) requirement |
| `h1` / `h2` / `h3` / `h4` | int \| `"min-max"` string | magic header values overriding WireGuard's four message types. Either a single uint32 (`1234567890`, AWG 1.x) or an inclusive range string (`"43613244-384550127"`, AWG 2.0 ranged headers) — the device picks a random value from the range per message. `0` **or** `""` = unset (counts as the WG default `1`/`2`/`3`/`4`) |
| `i1` … `i5` | string | default `""`. AWG 2.0 CPS decoy packets, **case-sensitive** tag-format strings, sent in order before the handshake. `i1` typically mimics a real protocol (e.g. a QUIC/STUN header) and is **mutually exclusive with the [`id`/`ip`/`ib`](#masquerade-id--ip--ib-wiresock-style-sugar-over-i1) sugar**. Tags: `<b 0xHEX>` static bytes, `<c>` counter, `<t>` timestamp, `<r N>` random bytes, `<rc N>` random chars, `<rd N>` random digits |

> **Ranged headers (AWG 2.0):** the four `h1`–`h4` ranges (an unset header counts as its WireGuard default — `1`/`2`/`3`/`4`) **must not overlap**, or the device rejects the config with `headers must not overlap`. Set all four together, as awg2 exports do. A plain number `N` is equivalent to the range `"N-N"`; `0` means unset.

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

### Masquerade `id` / `ip` / `ib` (WireSock-style sugar over `i1`)

Hand-writing an `i1` CPS string is fiddly. As a friendlier alternative — the same
naming [WireSock Secure Connect](https://www.wiresock.net/) uses — you can declare
a masquerade by **domain / protocol / browser** and the device generates the `i1`
decoy for you:

| Key | Type | Meaning |
|-----|------|---------|
| `id` | string | masquerade **domain** (a host that looks normal for your region, e.g. `www.google.com`). Strict LDH hostname (letters/digits/`-`/`_`, labels ≤63, total ≤253). It is embedded into the decoy for `ip=quic` (as the **ClientHello SNI**), `ip=dns` (as the QNAME) and `ip=sip` (as the host) — only `ip=stun` has nowhere to carry a hostname and ignores it. **Required only for `quic`; for `dns`/`sip` a pseudo name is generated when absent; `stun` ignores it.** Whenever set, it is LDH-validated (invalid/injection-y values are **rejected**) |
| `ip` | string | masquerade **protocol**: `quic` \| `dns` \| `stun` \| `sip` |
| `ib` | string | masquerade **browser**: `chrome` \| `firefox` \| `curl`. Only meaningful with `ip=quic`, and even then the effect is **minimal** (see note) |

The decoy is sent before the handshake, exactly like a hand-written `i1`. Each
profile is a **client-initiated** packet shaped like that protocol (the shapes are
inspired by the open-source WireSock reference, `amneziawg-proxy/src/transform.rs`,
but emitted as the client request a peer actually sends first, not a server
response); `quic` is a purpose-built RFC 9001 QUIC Initial that bypasses line-rate
DPI:

- **`quic`** — a full **QUIC Initial (RFC 9001)** carrying a realistic browser-shaped
  ClientHello (with your `id` as the SNI) **split across several out-of-order CRYPTO
  frames**: the first frame on the wire starts mid-ClientHello (offset≠0), so a
  line-rate DPI that grabs the first frame and assumes offset 0 parses garbage and
  fails open, while a real QUIC server reorders the frames normally. The layout is
  randomized per call (no fixed cross-user signature), and `ip=quic` fills **both `i1`
  and `i2`** with two independent Initials so the flow reads as a developing QUIC
  session. This is the device-proven DPI bypass (a plain QUIC short header was
  empirically blocked).
- **`dns`** — a client DNS **query** (QR=0, QTYPE HTTPS) whose QNAME is your `id`,
  carrying random cover bytes as an opaque unknown EDNS option.
- **`stun`** — a WebRTC STUN **Binding Request** (magic cookie + USERNAME +
  ICE-CONTROLLING + PRIORITY + SOFTWARE + MESSAGE-INTEGRITY + FINGERPRINT).
- **`sip`** — a SIP **INVITE request** with an SDP offer (request-line + Via/
  Max-Forwards/From/To/Call-ID/CSeq/Contact + `m=audio`), using your `id` (or a
  generated pseudo-host) as the host and pronounceable pseudo user names.

```jsonc
{
  "type": "wireguard", "tag": "awg-out", "mtu": 1280,
  "address": ["10.0.0.2/32"], "private_key": "<client-private-key-base64>",
  "jc": 4, "jmin": 40, "jmax": 70,
  "id": "www.google.com", "ip": "quic", "ib": "chrome",
  "peers": [ { "address": "engage.cloudflareclient.com", "port": 2408,
    "public_key": "<server-public-key-base64>", "allowed_ips": ["0.0.0.0/0", "::/0"] } ]
}
```

> **Notes & limitations.**
> - `id`/`ip`/`ib` are **mutually exclusive** with an explicit `i1` — set one or the
>   other, not both (a config with both is rejected).
> - This is a **decoy** sent before the handshake, not a full protocol session — the
>   `quic` Initial never completes a TLS handshake (it only needs to make the first
>   packet of the flow look like a legitimate QUIC start). The `id` **is** placed on
>   the wire as the ClientHello SNI (a DPI that publicly decrypts the Initial can read
>   it), so pick a **plausible, allowed** domain — never a VPN/Cloudflare marker.
> - The DPI bypass rests on **CRYPTO-frame fragmentation, not on a TLS/JA3
>   fingerprint** — we do not imitate a specific browser fingerprint. `ib` is accepted
>   for WireSock config compatibility and validated, but currently does not change the
>   generated ClientHello.
> - `id` is carried on the wire for `quic` (SNI), `dns` (QNAME) and `sip` (host); only
>   `ip=stun` produces a hostname-less decoy regardless of `id`.
> - The motivating use case is easing connections to **Cloudflare WARP**.

**📖 [Detailed examples →](../SPECS/009-WIRESOCK_MASQUERADE_PROFILES/EXAMPLES.md)** —
full per-profile configs (incl. a Cloudflare WARP one), the generated CPS for each,
a "which profile to pick" guide and a troubleshooting table of the exact validation
errors.

### MTU

AmneziaWG's `s3`/`s4` prepend junk bytes to **every transport message**, so an AWG endpoint needs a **lower `mtu` than plain WireGuard**. If the obfuscated packet exceeds the path MTU, the OS rejects it and the tunnel completes its handshake but **cannot send data**:

```
peer(…) - received handshake response
peer(…) - failed to send data packets: write udp4 …: sendmsg: message too long
```

Budget the overhead against a 1500-byte path:

```
mtu ≤ 1500 − 28 (UDP/IP) − 32 (WireGuard) − max(S3, S4) junk bytes
```

For `S3 = S4 = 60` that is `mtu ≤ 1380`. **Use `1280`** (the AmneziaWG-recommended client MTU) for headroom on smaller path MTUs (PPPoE, nested tunnels). This is unrelated to the handshake — a too-high `mtu` lets the handshake succeed but silently breaks data transfer.

**What sing-box-lx does for you:** if you omit `mtu` on an endpoint that sets `s3`/`s4`, the core defaults to **`1280`** (instead of the plain-WireGuard `1408`). If you set `mtu` explicitly and it is too high for the junk overhead, the core logs a startup warning — against a conservative **1492**-byte (PPPoE) budget, `mtu ≤ 1492 − 28 − 32 − max(S3, S4)`, so it may flag a value a few bytes below the 1500-byte Ethernet ceiling. The warning is advisory; the tunnel still loads.

Also keep `jmax` **below** the real path MTU: amneziawg-go warns that if a junk packet's size reaches the system MTU it gets IP-fragmented, which the same constrained paths then drop. Junk/signature params (`jc`, `s1`–`s4`, `i1`–`i5`) are client-side configuration only.

Map an `awg.conf` / awg-quick file 1:1: `[Interface] PrivateKey/Address/Jc/Jmin/Jmax/S1–S4/H1–H4/I1–I5` → endpoint root; `[Peer] PublicKey/PresharedKey/Endpoint/AllowedIPs/PersistentKeepalive` → `peers[0]` (`Endpoint host:port` → `address`+`port`). An `H1 = N` line maps to JSON number `N`, a ranged `H1 = N-M` line (awg2 export) maps to JSON string `"N-M"` verbatim. If the `awg.conf` omits `MTU` or sets the WireGuard-default `1420`, lower it for AWG2 (see [MTU](#mtu) above).

The runtime is backed by `Leadaxe/wireguard-go` (sagernet/wireguard-go + AmneziaWG obfuscation, wired via the `submodules/wireguard-go` submodule) — see SPECS/003.

---

## 3. round_robin load balancing (SPEC 019)

Upstream `urltest` always selects the single lowest-delay node. sing-box-lx adds a
`round_robin` **mode** that rotates traffic over a fixed-size **pool** of nodes — built to
scale to large node lists (only the pool is health-checked, not every node). Selection
happens once per connection; a UDP/QUIC session stays on its node. With `mode` omitted (or
`least_test`) the outbound behaves exactly like upstream and `balancer` must not be set.

The `GetPool` CommandClient method (see [§4](#4-observability-commandclient-extensions)) is
behind `with_lx_command`; the `mode`/`balancer` config fields themselves are always available.

### Fields (on a `urltest` outbound)

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `mode` | string | `least_test` | `least_test` (upstream behaviour) \| `round_robin` (rotate over the pool). `least_connection` is rejected (round_robin is statistically even) |
| `balancer` | object | — | round_robin parameters; **only valid with `mode: round_robin`** (error otherwise). The upstream `tolerance` field is ignored in round_robin (a warning is logged) — use `pool_tolerance` |

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

## 4. Observability (CommandClient extensions)

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
- **`SubscribeDNSQueries(includeAnswers, handler)`** — a structured live DNS-query stream
  (SPEC 018): per query the `domain`, `qtype`, `rcode` (**`-1` = lookup failure**, a
  first-class state), the CNAME chain / answers (when `includeAnswers`), process attribution,
  and `dnsServer` / `dnsServerType` / `outbound` (an empty `outbound` means direct/system —
  a valid state, not a bug).

SPEC 017 also enriches the existing connection stream: a tracked `Connection` now carries a
separate **`detourList`** field — the transport-detour tail of the final outbound, exposed
distinctly from the proxy `chain` (the chain omits the detour by design).

Build with the tag to get them:

```sh
make -f Makefile.lx lx-build   # includes with_lx_command (and with_xhttp/with_awg)
```

---

## 5. Validate & build

```sh
git clone --recurse-submodules <repo>           # with_awg needs the submodule
make -f Makefile.lx lx-build                     # builds ./sing-box with both features
./sing-box check -c lx-test/config/xhttp_reality.json
./sing-box check -c lx-test/config/awg2_basic.json

# Android (optional): libbox.aar with with_xhttp+with_awg baked in (needs NDK r28 + OpenJDK 17)
make lib_install && make lib_android             # → libbox.aar (SDK23) + libbox-legacy.aar (SDK21)
```

The CI (`.github/workflows/lx-ci.yml`) builds the feature matrix (`baseline` / `xhttp` / `awg` / `full`), a cross-platform matrix, **and the Android `libbox.aar`** (gomobile), running `check` on the matching sample configs. Pushing a `v*-lx.*` tag runs `lx-release.yml`, which publishes the desktop binaries **and** `libbox-<ver>.aar` / `libbox-legacy-<ver>.aar` as GitHub Release assets. A **Windows 7 (32-bit)** legacy binary (`sing-box-<ver>-windows-386-legacy-windows-7.zip`) is also published — built with a Win7-patched Go and **without `with_naive_outbound`** (`cronet-go` has no windows/386 build; every other feature is unchanged).
