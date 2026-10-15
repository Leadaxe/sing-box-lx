# FEATURE 018 — TRUSTTUNNEL — TrustTunnel protocol client (AdGuard)

> 🌐 Русская версия: **[FEATURE.ru.md](FEATURE.ru.md)**.
>
> **Language.** This feature came from outside (PR [#45](https://github.com/Leadaxe/sing-box-lx/pull/45), discussion in [#46](https://github.com/Leadaxe/sing-box-lx/issues/46)), so its documents use English as the primary language. The Russian copy (`*.ru.md`) sits next to each file to make the feature easier to maintain.

| Field | Value |
|-------|-------|
| Type | Product feature |
| Build tag | `with_trusttunnel` |
| State | N — draft for discussion ([#46](https://github.com/Leadaxe/sing-box-lx/issues/46)), task [133](../../TASKS/133-TRUSTTUNNEL_OUTBOUND/SPEC.md) |

## Purpose

TrustTunnel is the AdGuard VPN protocol, open-sourced in November 2025 (Apache-2.0,
specification `PROTOCOL.md` v1.0). From the outside it is ordinary HTTPS to a real
domain: HTTP/2 over TLS or HTTP/3 over QUIC, with a proxy `CONNECT` and Basic auth
inside. The protocol has its own server endpoint, official clients on every platform
and a growing base of self-hosted servers; mihomo, husi, Shadowrocket and Surge
already support it, upstream sing-box does not (request
[SagerNet/sing-box#3723](https://github.com/SagerNet/sing-box/issues/3723) has been
open since January 2026 with no maintainer response).

The feature adds a `trusttunnel` outbound: a TrustTunnel node in the core config on a
par with the others, carrying TCP and UDP, usable in groups, under `detour` and in
chains.

## Controlled parameters

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
  "tls": {
    "enabled": true,
    "server_name": "vpn.example.com",
    "utls": { "enabled": true, "fingerprint": "chrome" }
  },

  ... // Dial Fields
}
```

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `server`, `server_port` | string, number | required | Endpoint address |
| `username`, `password` | string | required | Credentials; sent as `proxy-authorization: Basic` on every `CONNECT` |
| `network` | `tcp` \| `udp` \| empty | empty = both | Which networks the node serves |
| `quic` | bool | `false` | `false` — HTTP/2 over TLS/TCP, `true` — HTTP/3 over QUIC |
| `quic_congestion_control` | `bbr` \| `cubic` \| `reno` | `bbr` | QUIC congestion control; ignored without `quic` |
| `health_check` | bool | `false` | Periodic `CONNECT _check` that keeps the session alive |
| `tls` | sing-box `tls` block | required, `enabled: true` | Shared TLS layer: `server_name`, `certificate`, `insecure`, `utls`, `fragment`, `ech` |
| Dial Fields | — | — | As for any outbound: `detour`, `bind_interface`, `domain_resolver`, … |

The key names match the sing-box TrustTunnel schema already used by husi and the
clients built on it: a config exported from them is read without renaming.

Mapping from the TrustTunnel endpoint export (`trusttunnel_endpoint … -c`,
subscription, `tt://` link) to the keys above:

| TrustTunnel | sing-box |
|-------------|----------|
| `hostname`, `custom_sni` | `tls.server_name` (`custom_sni` if set) |
| `addresses[]` | `server` / `server_port` (one node per address) |
| `username`, `password` | `username`, `password` |
| `certificate` | `tls.certificate` |
| `skip_verification` | `tls.insecure` |
| `upstream_protocol` `http2` / `http3` | `quic: false` / `true` |
| `anti_dpi` | `tls.fragment` (see "Boundaries") |
| client `tls_profile` | `tls.utls.fingerprint` |
| `client_random_prefix`, `client_random_auth_key` | none (see "Boundaries") |
| `dns_upstreams`, `has_ipv6`, `name` | outside the core — the client app's job |

Build: tag `with_trusttunnel`, built into the desktop binaries and the static router
builds; the Android AAR does not carry it. Without the tag `type: trusttunnel` is
rejected when the config is read, with a clear error, and the rest of the core behaves
exactly like upstream. In the UI (Clash API `type`, libbox) the node is labelled
"TrustTunnel".

## Inputs

- The node config.
- Connections from the router: TCP streams and UDP sessions with a destination
  (domain or IP).
- Network change events from the interface monitor.

## Outputs

- On the wire: one HTTP/2 or HTTP/3 connection to the endpoint; inside it every TCP
  connection is a separate `CONNECT host:port` and every UDP session is a separate
  `CONNECT _udp2` carrying framed datagrams.
- Dial errors carry the server's answer: the status code and the `X-Warning` header
  (connect failed, host unreachable, timeout, DNS non-routable) — in the error text,
  not a bare "EOF".
- Observability is the standard one for an outbound: `detourList`, URLTest, Clash API.

## Data flow

```
TCP connection ─► trusttunnel outbound
   │  CONNECT host:port, proxy-authorization: Basic, user-agent
   ▼
shared session to the endpoint (one per node; opened on the first request)
   │  HTTP/2: TLS via the shared layer (STD | uTLS), ALPN h2
   │  HTTP/3: QUIC v1, ALPN h3
   ▼
200 ─► the stream becomes a raw bidirectional byte channel
≠200 ─► dial error with the code and X-Warning

UDP session ─► resolve the destination domain (node dial fields / domain_resolver)
   ▼
CONNECT _udp2 ─► frames: length | src | dst | app name | payload
   ▼  reply frames: length | src | dst | payload

network change ─► the session is reset, the next requests open a new one
```

## Rules and guarantees

- **TLS is mandatory.** `tls.enabled: false` is a start error.
- **Credentials are mandatory.** An empty `username` or `password` is a start error.
- **The network is opened at start, not in the constructor.** The constructor only
  validates the config; the first connection to the server happens on the first request.
- **The server's answer is awaited before the dial returns.** A failed `CONNECT` is an
  error from `DialContext`, not a deferred error on the first read — otherwise groups
  (`urltest`, `failover`) cannot see the node fail.
- **UDP goes to IP addresses only.** The protocol carries a 16-byte address in the frame;
  the destination domain is resolved before the write via the node's dial fields.
- **HTTP/3 only with standard TLS.** A uTLS fingerprint with `quic: true` is a start
  error: the QUIC handshake is built on the STD config.
- **Idle does not wake the radio.** `health_check` is off by default; when on, it checks
  the session only while connections go through the node
  ([ENERGY](../008-ENERGY/FEATURE.md)).
- **A network change resets the session**, otherwise it hangs until the QUIC or TCP
  idle timeout.

## Boundaries

- **Client only.** Inbound/server is out of the feature: the protocol has its own
  official server, and the server side is second priority under CONSTITUTION §3.1.
- **ICMP is not tunnelled.** The protocol supports `CONNECT _icmp` (echo only, without
  payload), but in the core that is a separate ICMP routing path; deferred until requested.
- **No `upstream_protocol: auto`.** The official client races H2 against H3; here there is
  one choice per node (`quic`), and "auto" is a `urltest` group of two nodes.
- **No `client_random` rules.** A server can admit only ClientHellos with a given prefix
  or key in `client_random`. Nodes with such rules are not served by this feature;
  supporting them needs control of `client_random` through uTLS and will be a separate
  task when a real request appears.
- **`anti_dpi` is not a separate key.** In the official client it is a delay between the
  first byte of the ClientHello and the rest; the closest built-in equivalent is
  `tls.fragment`.
- **`tt://` links and TrustTunnel subscriptions are not parsed by the core.** Import is
  the job of the launcher and LxBox; the core receives ready JSON.
- **No multiplexing beyond one session.** All streams of a node go into one HTTP/2 or
  HTTP/3 connection; there is no pool and no rotation.

## Feature tasks

| Task | What | State |
|------|------|-------|
| [133](../../TASKS/133-TRUSTTUNNEL_OUTBOUND/SPEC.md) | `trusttunnel` outbound: TCP + UDP over HTTP/2 and HTTP/3 | N |

## Maintenance notes

- The protocol belongs to AdGuard; the specification is `TrustTunnel/TrustTunnel/PROTOCOL.md`.
  The wire format is checked against it and against the official client
  `TrustTunnel/TrustTunnelClient`, not against third-party implementations.
- The server compares the authorization byte for byte with standard base64 **with
  padding** of `user:pass`; any deviation in the encoding is a 407 with correct credentials.
- The magic `:authority` values (`_udp2`, `_check`, `_icmp`) are compared literally by the
  server, without a port.
