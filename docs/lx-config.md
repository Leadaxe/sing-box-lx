# sing-box-lx — configuration of the two downstream features

`sing-box-lx` is upstream [sing-box](https://github.com/SagerNet/sing-box) plus exactly two **client-side** features, each gated behind a build tag:

| Feature | Build tag | Where it lives in config |
|---------|-----------|--------------------------|
| **XHTTP** transport (Xray-compatible) | `with_xhttp` | `transport.type: "xhttp"` on a VLESS / VMess / Trojan outbound |
| **AmneziaWG 2.0** (AWG2) | `with_awg` | extra fields on a `wireguard` **endpoint** |

Build the binary with both: `make -f Makefile.lx lx-build` (output `sing-box`, version `…-lx.N`).
Without a tag the feature is absent: an `xhttp` transport or an AWG field is rejected at load time with an explicit error (no silent downgrade).

> ⚠️ All keys/UUIDs below are **placeholders**. Never commit real private keys / pre-shared keys to a repository.

---

## 1. XHTTP transport

XHTTP (Xray "splithttp"/"xhttp") is a v2ray transport that tunnels the proxy over plain HTTP/2 requests. It attaches to VLESS / VMess / Trojan via the shared `transport` block and composes with TLS, including **Reality**. (XHTTP is incompatible with XTLS-Vision — that is a protocol limitation, not ours.)

### Fields (`transport`)

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `type` | string | — | must be `"xhttp"` |
| `mode` | string | `auto` | `auto` \| `packet-up` \| `stream-up` \| `stream-one`. **`auto` uses `packet-up`** (live-validated against Xray/3x-ui). `stream-one` has a known downlink-framing bug — select it explicitly only if you know the server needs it. |
| `host` | string | TLS SNI / server | overrides the HTTP `Host` header |
| `path` | string | `/` | request path prefix; the random session id (and, for `packet-up`, the upload sequence number) are appended |
| `headers` | object | — | extra request headers sent on every XHTTP request |
| `x_padding_bytes` | string | `"100-1000"` | inclusive byte range of the `X-Padding` value (`"min-max"` or a single int) to blur the on-wire size |
| `no_grpc_header` | bool | `false` | omit Xray's gRPC-style headers in some modes (forward-compat) |

> **Note (wire format):** padding is carried as `x_padding=<zeros>` inside the `Referer` header (Xray's default placement) — live-validated against a real Xray (3x-ui) server. The server validates the `x_padding` length (default 100–1000) and replies `400` without it. Client and server Xray versions should still match (XHTTP evolves quickly).

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

AWG2 = AWG1 fields **plus** the CPS packets `I1`–`I5`. Both client and server must run AmneziaWG with **matching** parameters (the I-packets are configuration, not negotiated).

### Fields (on the `wireguard` endpoint, alongside `private_key`/`peers`/…)

| Key | Type | Meaning |
|-----|------|---------|
| `jc` | int | number of junk packets sent before the handshake |
| `jmin` / `jmax` | int | min / max size of those junk packets |
| `s1` / `s2` | int | junk prepended to the init / response handshake messages |
| `s3` / `s4` | int | junk prepended to the cookie-reply / transport messages (AWG 2.x) |
| `h1` / `h2` / `h3` / `h4` | int | magic header values overriding WireGuard's four message types |
| `i1` … `i5` | string | AWG 2.0 CPS decoy packets, **case-sensitive** tag-format strings, sent in order before the handshake. `I1` typically mimics a real protocol (e.g. a QUIC/STUN header). Tags: `<b 0xHEX>` static bytes, `<c>` counter, `<t>` timestamp, `<r N>` random bytes, `<rc N>` random chars, `<rd N>` random digits |

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

Map an `awg.conf` / awg-quick file 1:1: `[Interface] PrivateKey/Address/Jc/Jmin/Jmax/S1–S4/H1–H4/I1–I5` → endpoint root; `[Peer] PublicKey/PresharedKey/Endpoint/AllowedIPs/PersistentKeepalive` → `peers[0]` (`Endpoint host:port` → `address`+`port`). If the `awg.conf` omits `MTU` or sets the WireGuard-default `1420`, lower it for AWG2 (see [MTU](#mtu) above).

The runtime is backed by `Leadaxe/wireguard-go` (sagernet/wireguard-go + AmneziaWG obfuscation, wired via the `submodules/wireguard-go` submodule) — see SPECS/003.

---

## 3. Validate & build

```sh
git clone --recurse-submodules <repo>           # with_awg needs the submodule
make -f Makefile.lx lx-build                     # builds ./sing-box with both features
./sing-box check -c lx-test/config/xhttp_reality.json
./sing-box check -c lx-test/config/awg2_basic.json

# Android (optional): libbox.aar with with_xhttp+with_awg baked in (needs NDK r28 + OpenJDK 17)
make lib_install && make lib_android             # → libbox.aar (SDK23) + libbox-legacy.aar (SDK21)
```

The CI (`.github/workflows/lx-ci.yml`) builds the feature matrix (`baseline` / `xhttp` / `awg` / `full`), a cross-platform matrix, **and the Android `libbox.aar`** (gomobile), running `check` on the matching sample configs. Pushing a `v*-lx.*` tag runs `lx-release.yml`, which publishes the desktop binaries **and** `libbox-<ver>.aar` / `libbox-legacy-<ver>.aar` as GitHub Release assets.
