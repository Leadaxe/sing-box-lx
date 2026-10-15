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
chains. It also adds a `trusttunnel` inbound: a sing-box instance can be the endpoint
for the official clients and for the outbound above (owner 2026-10-15; task 134).

The protocol itself is written as a self-contained package with its own API, client and
server, with no dependency on sing-box internals, to the bar of an upstream `sing-*`
library: it can be sent upstream by someone else or taken by husi/NekoBox as is. sing-box
sees only thin adapters over it, and those files are laid out in upstream form
(`option/trusttunnel.go`, `protocol/trusttunnel/`, `include/trusttunnel.go`), not with the
fork's `_lx` naming (owner 2026-10-15; SPEC 133 §5).

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
  "idle_timeout": "5m",
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
| `server`, `server_port` | string, number | required | Endpoint address. An IP literal in `server` requires `tls.server_name`: the endpoint routes by SNI only |
| `username`, `password` | string | required | Credentials; sent as `proxy-authorization: Basic` on every `CONNECT` |
| `network` | `tcp` \| `udp` \| empty | empty = both | Which networks the node serves |
| `quic` | bool | `false` | `false` — HTTP/2 over TLS/TCP, `true` — HTTP/3 over QUIC |
| `quic_congestion_control` | `bbr` \| `cubic` \| `reno` | `bbr` | QUIC congestion control; ignored without `quic` |
| `health_check` | bool | `false` | `CONNECT _check` on events (a dial after silence, network change, wake-up), throttled; never a timer. See "Rules" |
| `idle_timeout` | duration | unset = global default | Close the session locally after this long without a request; `0` keeps it. Same key as on the MASQUE outbound (SPEC 133, Q7) |
| `tls` | sing-box `tls` block | required, `enabled: true` | Shared TLS layer: `server_name`, `certificate`, `insecure`, `utls`, `fragment`, `ech` |
| Dial Fields | — | — | As for any outbound: `detour`, `bind_interface`, `domain_resolver`, … |

The key names match the sing-box TrustTunnel schema already used by husi and the
clients built on it: a config exported from them is read without renaming
(`idle_timeout` is the one key added here; husi also accepts an empty `password`, this
feature does not).

Mapping from the TrustTunnel endpoint export (`trusttunnel_endpoint … -c`,
subscription, `tt://` link) to the keys above:

| TrustTunnel | sing-box |
|-------------|----------|
| `hostname`, `custom_sni` | `tls.server_name` (`custom_sni` if set) |
| `addresses[]` | `server` / `server_port` (one node per address) |
| `username`, `password` | `username`, `password` |
| `certificate` | `tls.certificate` (the deep link carries a DER chain, the TOML export PEM; sing-box takes PEM, the importer converts) |
| `skip_verification` | `tls.insecure` |
| `upstream_protocol` `http2` / `http3` | `quic: false` / `true` |
| `anti_dpi` | `tls.fragment` (see "Boundaries") |
| client `tls_profile` | `tls.utls.fingerprint` |
| `client_random_prefix`, `client_random_auth_key` | none (see "Boundaries") |
| `has_ipv6: false` | `domain_resolver: { strategy: "ipv4_only" }` on the node (the endpoint refuses IPv6 destinations) |
| `dns_upstreams`, `name` | outside the core — the client app's job |

### Server side (sketch; the contract is in task 134)

```json
{
  "type": "trusttunnel",
  "tag": "tt-in",
  "listen": "::",
  "listen_port": 443,
  "users": [ { "username": "user", "password": "secret" } ],
  "network": "",
  "tls": { "enabled": true, "certificate_path": "…", "key_path": "…" }
}
```

The inbound serves what the official clients send: TCP `CONNECT`, `CONNECT _udp2` with
the protocol's frames, `CONNECT _check`, Basic auth with 407 + `proxy-authenticate` on
refusal; HTTP/2 always, HTTP/3 when the build has `with_quic`. Routing, access rules and
address filtering are sing-box's route rules, not endpoint settings. What the official
endpoint offers beyond the tunnel — rules by `client_random`, subscription, ping and
speed-test hosts, reverse proxy, ICMP, SOCKS5 forwarding — is out of the feature.

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
  (connect failed, host unreachable, timeout, DNS non-routable / loopback with the
  hostname) — in the error text, not a bare "EOF".
- The node identifies itself to the endpoint as `user-agent: <platform> <app>`
  (platform from the OS, `app` a fixed application string) and repeats the same string in
  every UDP frame; the originating process name is never sent. The endpoint logs the
  user agent and may pass it to its SOCKS5 upstream.
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
idle > idle_timeout ─► the session is closed locally, without packets
stale session (closed by the endpoint, NAT expired, device slept)
   ─► the dial that finds it rebuilds the session and repeats CONNECT once
```

## Rules and guarantees

- **TLS is mandatory.** `tls.enabled: false` is a start error. **SNI is mandatory**: with an
  IP literal in `server` and no `tls.server_name` the config is rejected at start, because
  the endpoint answers an unknown or missing SNI with a TLS drop and nothing else.
- **Credentials are mandatory.** An empty `username` or `password` is a start error. A
  refusal (407 or the endpoint's configured code) is a dial error **and** tears the session
  down, as the protocol requires; a GOAWAY with the protocol's "authentication required"
  code is reported as such.
- **The network is opened at start, not in the constructor.** The constructor only
  validates the config; the first connection to the server happens on the first request.
- **The server's answer is awaited before the dial returns.** A failed `CONNECT` is an
  error from `DialContext`, not a deferred error on the first read — otherwise groups
  (`urltest`, `failover`) cannot see the node fail.
- **UDP goes to IP addresses only.** The protocol carries a 16-byte address in the frame;
  the destination domain is resolved before the write via the node's dial fields. TCP
  domains, by contrast, go to the endpoint unresolved and are resolved there: the two
  networks may see different DNS answers, and a `domain_resolver` that is not itself
  detoured through the node resolves UDP destinations outside the tunnel.
- **HTTP/3 only with standard TLS.** A uTLS fingerprint with `quic: true` is a start
  error: the QUIC handshake is built on the STD config.
- **Idle does not wake the radio** ([ENERGY](../008-ENERGY/FEATURE.md)). No HTTP/2 PING,
  no QUIC keepalive, no timers while the node is idle, whatever the config. After
  `idle_timeout` the session is closed locally without a packet. `health_check` is off by
  default; when on, it is sent on events (a dial after silence, a network change, wake-up)
  with a throttle, never on a schedule. The official client keeps its HTTP/3 session alive
  with a packet every 30 s; this feature deliberately does not.
- **A stale session is the node's problem, not the caller's.** The endpoint closes a session
  after 10 min without a request, NAT drops idle TCP, the device sleeps. The dial that finds
  the session dead or stale rebuilds it and repeats the `CONNECT` once within the same
  deadline; `urltest` and `failover` never see a "dead node" that was only a dead session.
- **One session, no more.** The endpoint caps streams per session (1000 on HTTP/2 by
  default). The node does not open a second session to get around the cap; a dial above the
  cap waits for a free stream within its deadline.
- **A network change resets the session**, otherwise it hangs until the QUIC or TCP
  idle timeout; the endpoint has QUIC migration off by default.

## Boundaries

- **The server is a tunnel endpoint, not the AdGuard product.** The inbound speaks the
  protocol and hands connections to the router; everything the official endpoint does
  around the tunnel (rules, subscription, ping, speed test, reverse proxy) stays with the
  official endpoint.
- **ICMP is not tunnelled.** The protocol supports `CONNECT _icmp` (echo only, without
  payload), but in the core that is a separate ICMP routing path; deferred until requested.
- **No `upstream_protocol: auto`.** The official client races H2 against H3; here there is
  one choice per node (`quic`), and "auto" is a `urltest` group of two nodes.
- **No `client_random` rules.** A server can admit only ClientHellos with a given prefix
  or key in `client_random`. Nodes with such rules are not served by this feature; the
  symptom is a TLS drop with no message, the same as a wrong SNI, so the only way to tell
  is the exported config. Supporting them needs control of `client_random` through the
  uTLS fork and is possible for HTTP/2 only (HTTP/3 runs on the standard TLS stack); the
  official client itself has it only on AdGuard-patched OpenSSL. A separate task when a
  real request appears.
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
| [133](../../TASKS/133-TRUSTTUNNEL_OUTBOUND/SPEC.md) | `trusttunnel` outbound: TCP + UDP over HTTP/2 and HTTP/3; the protocol core package (client half) | N |
| 134 — TRUSTTUNNEL_INBOUND | `trusttunnel` inbound on the server half of the core package; SPEC not written yet | planned |

## Maintenance notes

- The protocol belongs to AdGuard; the specification is `TrustTunnel/TrustTunnel/PROTOCOL.md`.
  The wire format is checked against it and against the official client
  `TrustTunnel/TrustTunnelClient`, not against third-party implementations.
- The protocol core package must stay free of sing-box imports (`adapter`, `option`, `log`):
  that boundary is what makes it liftable into its own module and usable by other clients.
  A review that finds a sing-box type in the core is a review that fails.
- Tests: unit tests in the core package (client against the package's own server), stands in
  `lx-test/` against the official endpoint. Upstream-style `test/` suites are left to whoever
  submits the protocol upstream (owner 2026-10-15).
- The server compares the authorization byte for byte with standard base64 **with
  padding** of `user:pass`; any deviation in the encoding is a 407 with correct credentials.
- The magic `:authority` values (`_udp2`, `_check`, `_icmp`) are compared literally by the
  server, without a port. A TCP `CONNECT` without a port in the authority is an error.
- Endpoint defaults that shape the client: a session with no request for
  `client_listener_timeout_secs` (600) is closed; `max_concurrent_streams` 1000 per HTTP/2
  session; `allow_private_network_connections = false` refuses private destinations
  (a stand with local targets must enable it); QUIC active migration is off.
- The health-check numbers in `PROTOCOL.md` are easy to misread: 7 s is the response
  deadline, the throttle is the connection timeout (30 s), and the trigger is tunnel
  events, not a clock. `sing-trusttunnel` turned 7 s into a timer interval.
