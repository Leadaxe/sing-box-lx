# SPEC 134 — `trusttunnel` inbound: the server half of the protocol

> 🌐 Русская версия: **[SPEC.ru.md](SPEC.ru.md)**.

**Feature:** [TRUSTTUNNEL](../../FEATURES/018-TRUSTTUNNEL/FEATURE.md)

| Field | Value |
|-------|-------|
| Type | F (feature) — server inbound |
| Status | I (implemented) — released in v1.14.3-lx.21 (2026-10-16) together with SPEC 133, in `LX_TAGS`, desktop and router binaries; verified by the owner on a live link 2026-10-29, task closed. The shape (inbound, not endpoint), the two-layer layout and the file form are inherited from SPEC 133; §7 Q1–Q5 shipped as proposed and accepted with the release |
| Branch | `lx` |
| Build tag | `with_trusttunnel` (HTTP/3 half additionally needs `with_quic`) |
| Discussion | [Leadaxe/sing-box-lx#46](https://github.com/Leadaxe/sing-box-lx/issues/46) |
| Origin | Owner 2026-10-15: the server side is written right away, not on request (SPEC 133 §7 Q6) |
| Depends on | [133](../133-TRUSTTUNNEL_OUTBOUND/SPEC.md) — the protocol core package `transport/trusttunnel/`, options file, registration, constant; this task adds the server half to the same package and the `inbound.go` adapter |
| Related | [CHAIN](../../FEATURES/015-CHAIN/FEATURE.md) · [UPSTREAM_SYNC](../../FEATURES/005-UPSTREAM_SYNC/FEATURE.md) (removal condition) · upstream `protocol/naive` (the in-tree model) |

## 0. TL;DR

A `trusttunnel` inbound: a sing-box instance accepts sessions from the official
TrustTunnel clients and from the outbound of SPEC 133, authenticates them with Basic auth
against `users[]`, and hands every TCP `CONNECT` and every UDP flow of a `CONNECT _udp2`
stream to the router like any other inbound. HTTP/2 over the listener's TLS always, HTTP/3
over QUIC when the build has `with_quic`. It is an **inbound, not an endpoint**: the
protocol is L4 (a `CONNECT` per TCP connection, UDP frames with addresses inside), the
server never dials towards its clients and gives them no addresses, so it sits with
`naive` and `anytls`, not with WireGuard or the upstream `masque-server` endpoint, which
terminate IP packets. The server is a tunnel terminator, not the AdGuard product: rules by
`client_random`, subscription, ping and speed-test hosts, reverse proxy, ICMP and SOCKS5
forwarding stay with the official endpoint.

## 1. Problem and §3.1 criteria

- **(a1) Needed.** Owner's decision 2026-10-15. Two uses: a self-hosted node that the
  official AdGuard clients (all platforms) can connect to with nothing but sing-box on the
  server, and the second end for testing the outbound of SPEC 133 end to end on our own
  stand without the Rust endpoint.
- **(a2) Upstream does not provide it.** No `trusttunnel` inbound in `upstream/stable`,
  `testing` or `unstable` (checked 2026-10-15).
- **(a3) Cheaper than the alternative.** The alternative is the official
  `trusttunnel_endpoint` next to sing-box, a second daemon with its own TLS, credentials and
  no sing-box routing. The cost here is zero additional lines in upstream files beyond those
  already counted in SPEC 133 §5: the registration block for the inbound registry and the
  shared constant are part of that count.
- **(b) Isolation** — §5.
- **(c) Spec Kit** — feature [018](../../FEATURES/018-TRUSTTUNNEL/FEATURE.md) first, then
  133 for the client and this task for the server.

## 2. Protocol facts the server has to honour (per `PROTOCOL.md` v1.0, the endpoint source and the official client)

SPEC 133 §2 is the wire reference; only what the server side adds is listed here.

### 2.1 What the official client sends

- TLS with SNI; ALPN `h2` on HTTP/2, `h3` on HTTP/3. The client drops a session where
  anything other than its expected ALPN was negotiated.
- `CONNECT host:port` with `user-agent: <platform> <app>` and
  `proxy-authorization: Basic …` on every request; `CONNECT _udp2`, `CONNECT _check`,
  `CONNECT _icmp` with the same headers (user agent `<platform> _udp2` / `<platform>` /
  `<platform> _icmp`).
- HTTP/2 SETTINGS: initial stream window 131072; the client expects the server to accept up
  to `max_concurrent_streams` streams (official default 1000).
- With `upstream_protocol: auto` the client races HTTP/3 against HTTP/2 with a 1 s delay and
  keeps whichever session opens first; a server that offers only one of them is normal.
- The client keeps an HTTP/3 session alive with ACK-eliciting packets every 30 s; on HTTP/2
  it sends `CONNECT _check` on tunnel events, throttled to once per 30 s.

### 2.2 What the official endpoint answers

| Request | Answer |
|---------|--------|
| `CONNECT host:port`, auth ok, upstream connected | 200, then the stream is a raw channel; **200 is sent after the upstream connect succeeded** |
| `CONNECT host:port`, upstream failed | 502 + `X-Warning: 300/301/302/310/311 - …`, plus `X-Adguard-Vpn-Error: <host>` for 310/311 |
| `CONNECT` without a port in the authority | request error |
| auth failed (any `CONNECT`) | 407 + `proxy-authenticate: Basic realm=Authorization Required`; `auth_failure_status_code` can turn it into 403/404/405 |
| `CONNECT _udp2`, auth ok | 200 at once, then frames both ways; flows tracked per stream by the 4-tuple, idle 300 s by default config (120 s in the specification) |
| `CONNECT _check`, auth ok | 200 at once, stream closed |
| non-`CONNECT` request on the main host | the tunnel channel, unless a ping/speedtest/subscription/reverse-proxy path matches; refused with `non_connect_auth_failure_status_code` |
| session silent for `client_listener_timeout_secs` (600) | session closed (HTTP/2: connection close; HTTP/3: QUIC idle timeout) |

The endpoint compares the `proxy-authorization` value as a string against the standard
base64 of `user:pass`; any other encoding of the same credentials is a refusal.

### 2.3 What sing-box's router gives an inbound

`RouteConnectionEx(ctx, conn, metadata, onClose)` dials the outbound and pipes; the inbound
gets no "dial succeeded" callback. It does get two signals usable as one: `onClose(err)` is
called when the router gives up (dial error included), and the router's first `Read` on the
handed-over `conn` happens only after a successful dial, when the copy loop starts (or
earlier, when sniffing is enabled on this inbound and the router reads to sniff). The
upstream `naive` inbound ignores this and answers 200 before routing.

## 3. Config contract

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

| Key | Type | Default | Validation |
|-----|------|---------|------------|
| Listen Fields | `ListenOptions` | — | as for everyone (`listen`, `listen_port`, `detour`, `sniff`, …) |
| `users` | `[]auth.User` (`username`, `password`) | required | empty list — start error; empty `username` or `password` in an entry — start error |
| `network` | `NetworkList` (`tcp`/`udp`) | both | **which listeners to open**, as on the upstream `naive` inbound: `tcp` — HTTP/2 over TLS on the TCP listener, `udp` — HTTP/3 over QUIC on the UDP listener. `udp` without `with_quic` — start error when it is the only one, a warning when both are listed. Tunnelled networks are always both; what a client may reach is route rules (Q1) |
| `quic_congestion_control` | `string` enum `bbr,cubic,reno` | `bbr` | unknown — start error; ignored without `udp` |
| `tls` | `InboundTLSOptions` | required, `enabled: true` | `enabled: false` — `C.ErrTLSRequired` (Q3 on h2c); ALPN is completed by the inbound: `h2` (TCP) is put first, `h3` (UDP) appended last, user values are kept in between; a value the user already lists keeps its place |

The schema matches husi's `TrustTunnelInboundOptions` (`ListenOptions`, `Users`,
`QUICCongestionControl`, `Network`, TLS container) except `icmp` in `network`, which is a
start error here. `TrustTunnelInboundOptions` lives in the same `option/trusttunnel.go` as
the outbound options (SPEC 133 §5).

## 4. Requirements

- **R1. Listeners.** `Start(StartStateStart)`: TLS config started and scoped; with `tcp`
  HTTP/2 over the TLS listener (the core's own accept loop: TLS handshake, ALPN must be
  `h2`, HTTP/1.1 is not served), ALPN `h2`; with `udp` HTTP/3 over the UDP listener. The H3
  half is gated by `with_quic` inside the core package, which answers `ErrQUICNotIncluded`
  without it; the adapter learns that before opening the UDP listener, so no hook file is
  needed in `protocol/trusttunnel/` and `include/quic.go` is not touched. Server limits mirror the official endpoint: `MaxConcurrentStreams` 1000 on
  HTTP/2 (Go's default is 250 and would make the outbound's R11 wait at a quarter of the
  expected depth), session idle timeout 600 s on both transports, QUIC `MaxIncomingStreams`
  4096, 0-RTT off.
- **R2. Authentication.** Every request must carry `proxy-authorization: Basic`; the
  credentials are parsed and checked against `users[]` (a parsed comparison, so a
  non-canonical base64 of correct credentials is accepted where the endpoint would refuse it;
  this is the lenient direction and harmless). Refusal: 407 with
  `proxy-authenticate: Basic realm=Authorization Required` and the stream closed; logged with
  the source address, without the credentials. The username goes into `metadata.User`.
- **R3. TCP.** `CONNECT` with `:authority` `host:port` (IPv6 `[addr]:port`, a domain is
  passed to the router as a domain, unresolved) becomes a connection for
  `RouteConnectionEx` with `Source` = the session's remote address, `Destination` = the
  authority, `OriginDestination` = the listener address. An authority without a port is a
  400 and the stream is closed. The H2 stream is wrapped the way `naive` wraps its
  `naiveH2Conn` (body as reader, response writer with flush as writer, close waits for the
  router's `onClose`).
- **R4. The 200 is deferred to the dial result (proposal, Q2).** The handler answers
  **200 on the router's first `Read`** of the handed-over connection and **502 + `X-Warning`**
  if `onClose(err)` fires first, with `300 - Connection failed for some reason` as the
  generic code and `302` for a deadline error, so that the official client and our outbound
  (SPEC 133 R1) see a failed dial the same way they see it from the official endpoint. With
  `sniff` on this inbound the router reads before dialing: the 200 goes out early and a
  later dial failure is a plain stream close, as on `naive`. Alternative — 200 at once as
  `naive` does; then a dead destination is invisible to the client until the stream closes.
- **R5. UDP.** `CONNECT _udp2` is answered 200 at once. The stream is read frame by frame
  (§2.4 of SPEC 133; `len` bounded, a truncated or oversized frame closes the stream with a
  logged error). Flows are keyed per stream by the 4-tuple from the frame; a new tuple creates
  a virtual `N.PacketConn` handed to `RoutePacketConnectionEx` with `Source` = the frame's
  source if non-zero, else the session's remote address, `Destination` = the frame's
  destination. Writes to that conn are framed back with the sender as source and the flow's
  original source as destination, then written to the stream and flushed. A flow ends when
  the router closes it (sing-box's UDP timeout applies) or when the stream closes; closing
  the stream closes every flow of that stream. `app_name` is read, length-checked and
  ignored (not logged).
- **R6. `_check`.** With valid auth: 200 at once and the stream closed. No routing, no log
  line above debug.
- **R7. Not served.** `CONNECT _icmp`: 404 and the stream closed (the official client with
  ICMP enabled must keep working for TCP/UDP — verified on the stand, §6). Any non-`CONNECT`
  request: 404 with an empty body, without a `proxy-authenticate` header, so that a GET from
  a browser or a scanner sees a web server with nothing on it rather than a proxy (Q4 on
  going further).
- **R8. Lifecycle.** The constructor only validates; listeners open in `Start`; `Close`
  shuts both servers down with a bounded grace period and closes every stream and flow. No
  timers while no session is open.
- **R9. Core package API.** The server half lives in `transport/trusttunnel/` next to the
  client (SPEC 133 §5): a `Service` with handlers for TCP connections and UDP flows (each
  carrying the username), a user table that can be replaced at runtime, `NewService` +
  `ServeTCP(listener, tlsConfig)` / `ServeQUIC(packetConn, tlsConfig)` (each blocks until its
  listener fails or the service is closed; the adapter opens and owns the listeners and
  runs only the ones `network` asks for) / `Close()`, the H3 part under `with_quic`. No imports from
  sing-box: logging through `sing/common/logger`, TLS through `sing/common/tls` interfaces,
  users through `sing/common/auth`. `protocol/trusttunnel/inbound.go` maps options, listener,
  metadata and the router onto it.
- **R10. Without the tag.** The stub from SPEC 133 R8 covers the inbound type too.
- **R11. Documentation.** The inbound section next to the outbound in
  `docs-lx/protocols-transports.md` (SPEC 133 R12), with the config above and the three
  limits a server operator must know: no `client_random` rules, no subscription, non-tunnel
  paths answer 404.

## 5. Isolation and merge cost

Everything is inside the layout decided in SPEC 133 §5; this task adds files, not upstream
lines:

| What | Where | Upstream file? |
|------|-------|----------------|
| Server half of the core — `Service`, request handling, `_udp2` flow table, 407/502 answers | `transport/trusttunnel/service.go`, `service_udp.go`; `service_quic.go` under `with_quic` | no |
| sing-box adapter | `protocol/trusttunnel/inbound.go` (no `with_quic` file: the core gates H3) | no |
| Options | `TrustTunnelInboundOptions` in `option/trusttunnel.go` (shared with 133) | no |
| Registration / stub / constant | `include/trusttunnel.go`, `include/trusttunnel_stub.go`, the marked blocks in `include/registry.go` and `constant/proxy.go` — **already counted in SPEC 133 §5** | — |
| Tests | `*_test.go` next to the code; stand `lx-test/trusttunnel/` shared with 133 | no |

`include/quic.go`, `include/quic_stub.go` and `protocol/naive/*` are not touched; the
`naive` code is the model, not a dependency.

## 6. Acceptance criteria

1. Builds without `with_trusttunnel`, with it and without `with_quic` (H2 only), with both;
   the AAR without the tag; `go vet` under all sets.
2. Core package unit tests, no sing-box: client of SPEC 133 against this `Service`
   in-process over a loopback TLS listener — TCP round trip, UDP frames both ways (IPv4 and
   IPv6 flows on one stream, two flows to the same destination), wrong password → 407 seen
   by the client as a dial error, `_check` → 200, truncated frame closes the stream, 1000
   concurrent streams accepted.
3. Stand `lx-test/trusttunnel/`: this inbound on a self-signed certificate; through it —
   the **official client** (CLI build of `TrustTunnelClient`, `tt://` config pointing at the
   stand, `certificate` pinned) and **our outbound** (SPEC 133): HTTP(S) over TCP, DNS over
   UDP, a large UDP frame; HTTP/2 and HTTP/3; the official client with
   `upstream_protocol: auto` against an H2-only inbound.
4. R4 on the stand: a `CONNECT` to a closed port through our outbound is an error from
   `DialContext` with `X-Warning 300`, not a dead stream; the same with `sniff` on degrades
   to a stream close and nothing hangs.
5. R7 on the stand: `curl https://host/` gets 404 without `proxy-authenticate`; the official
   client with ICMP on keeps TCP/UDP working after its `_icmp` is refused.
6. Idle: a session with no request for 600 s is closed by the inbound; our outbound rebuilds
   transparently on the next dial (SPEC 133 §6 item 6, now against our own server).
7. A route rule on the inbound (`inbound: tt-in` + `ip_is_private: true` with
   `"action": "reject"`) stops a client from reaching the server's private network, which is
   the sing-box way to the endpoint's `allow_private_network_connections = false`.

## 7. Open questions (for discussion)

- **Q1. `network` semantics.** Proposal — as on the upstream `naive` inbound: which
  listeners to open (`tcp` = HTTP/2, `udp` = HTTP/3), tunnelled networks always both and
  restricted by route rules. This is the opposite of what `network` means on the outbound of
  SPEC 133 (tunnelled networks), but it is the existing convention for the closest upstream
  inbound and husi's option carries the same field. Alternative — a separate `transport`
  list and `network` for tunnelled networks.
- **Q2. Deferred 200 (R4).** Proposal — defer to the router's first read / `onClose`, with
  the documented degradation under `sniff`. Alternative — immediate 200 as `naive`. The
  deferred form is what makes a failed dial visible to the client and keeps the official
  client's error reporting intact; the cost is one wrapper around the H2/H3 stream.
- **Q3. Plain HTTP/2 (h2c) behind a TLS-terminating proxy.** The protocol requires TLS, but
  an operator with nginx/caddy in front may want `tls.enabled: false` on the TCP listener
  with h2c, which the `naive` handler already supports. Proposal — not in this task; keep
  `C.ErrTLSRequired` and open on request.
- **Q4. Active-probing posture.** The official endpoint lets the operator answer 403/404/405
  instead of 407 and serve real content on non-tunnel paths (reverse proxy, ping). Proposal
  — 407 for bad auth on `CONNECT`, 404 for everything else (R2, R7), no configuration key
  in this task; a `fallback`-style detour to a real web server (as `trojan`/`vless` inbounds
  have) is a separate question when someone asks.
- **Q5. Zero source in UDP frames.** Our outbound and mihomo send a zero source; the official
  client sends the real one. Proposal — accept both, key flows by the full tuple, and use the
  session's remote address as `metadata.Source` when the frame's source is zero (R5), so
  route rules by `source_ip` keep meaning the client.

## 8. Removal condition

Upstream sing-box adds a `trusttunnel` inbound in the surface the fork builds — removed
together with the outbound (SPEC 133 §8).

## 9. Sources

- `TrustTunnel/TrustTunnel` (commit `48f424c`): `PROTOCOL.md` §5–§9, `CONFIGURATION.md`
  (core settings, `auth_failure_status_code`, `client_listener_timeout_secs`,
  `max_concurrent_streams`, `allow_private_network_connections`), endpoint
  `lib/src/http_downstream.rs` (request routing, `fail_request`, `X-Warning` mapping,
  `proxy-authenticate`), `lib/src/http_demultiplexer.rs` (path channels),
  `lib/src/udp_forwarder.rs` (flow table), `lib/src/authentication/registry_based.rs`.
- `TrustTunnel/TrustTunnelClient` (commit `64e2e5b`): `core/src/utils.cpp` (request
  headers), `core/src/fallbackable_upstream_connector.cpp` (H3/H2 race),
  `core/src/http3_upstream.cpp` (idle keepalive).
- In-tree model: `protocol/naive/inbound.go`, `protocol/naive/quic/inbound_init.go`
  (H3 hook, congestion control switch); `protocol/hysteria2/inbound.go` and
  `protocol/tuic/inbound.go` (`RoutePacketConnectionEx` with per-flow packet conns).
- Third-party: `xchacha20-poly1305/sing-trusttunnel` v0.3.2 `service.go` (answers 403 on
  bad auth, 405 on non-`CONNECT`; a reference for the shape of the server API, not for the
  wire format).
