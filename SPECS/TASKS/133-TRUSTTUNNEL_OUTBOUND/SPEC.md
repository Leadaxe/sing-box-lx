# SPEC 133 — `trusttunnel` outbound: TCP + UDP over HTTP/2 and HTTP/3

> 🌐 Русская версия: **[SPEC.ru.md](SPEC.ru.md)**.

**Feature:** [TRUSTTUNNEL](../../FEATURES/018-TRUSTTUNNEL/FEATURE.md)

| Field | Value |
|-------|-------|
| Type | F (feature) — client outbound |
| Status | I (implemented) — released in v1.14.3-lx.21 (2026-10-16), in `LX_TAGS`, desktop and router binaries; verified by the owner on a live link 2026-10-29 (§6 item 7 closed), task closed. Decided: Q1 (in-tree, protocol core as a self-contained package), Q4 (desktop + routers), Q5 ("TrustTunnel" label), Q6 (the server side written right away, task 134), tests (`lx-test/` stands, upstream `test/` left to others); Q7–Q11 shipped as proposed and accepted with the release |
| Branch | `lx` |
| Build tag | `with_trusttunnel` |
| Discussion | [Leadaxe/sing-box-lx#46](https://github.com/Leadaxe/sing-box-lx/issues/46) |
| Origin | PR [Leadaxe/sing-box-lx#45](https://github.com/Leadaxe/sing-box-lx/pull/45) (closed: three protocols without a task, the build was broken); owner 2026-10-15: start with TrustTunnel |
| Related | [MASQUE_WARP](../../FEATURES/009-MASQUE_WARP/FEATURE.md) (in-tree H2/H3 client code, `idle_timeout` pattern) · [ENERGY](../../FEATURES/008-ENERGY/FEATURE.md) (no traffic while idle) · [CHAIN](../../FEATURES/015-CHAIN/FEATURE.md) (node as a link) · [UPSTREAM_SYNC](../../FEATURES/005-UPSTREAM_SYNC/FEATURE.md) (removal condition) |

## 0. TL;DR

A new `trusttunnel` outbound behind the `with_trusttunnel` tag: TCP as `CONNECT host:port`,
UDP as `CONNECT _udp2` with the protocol's frames, transport HTTP/2 over TLS or HTTP/3
over QUIC (`quic: true`). The key schema is the one already used by husi and its
derivatives. ICMP, `client_random` rules and `auto` H2/H3 selection are out of the task;
the inbound is the sibling task 134 of the same feature. The implementation is in-tree,
without a third-party library, in two layers: a self-contained protocol package with its
own API (client and server, no imports from sing-box `adapter`/`option`, fit to be lifted
into a `sing-*` style module or taken by husi/NekoBox as is) and thin sing-box adapters
over it (§5, §7 Q1). The session between requests is governed by the ENERGY rules: no
keepalive, no timers while idle; a stale session is rebuilt transparently by the next dial
(§4, R3).

## 1. Problem and §3.1 criteria

- **(a1) Needed.** The request came from a downstream client (NekoBox/NyameBox is building
  its next version on sing-box-lx and already carries the protocol in its fork — PR #45).
  Upstream has an open request, SagerNet/sing-box#3723 (January 2026, ~30 reactions, field
  reports: works in Russia, unstable in Iran). There is **no request yet** from the priority
  consumers — the launcher, LxBox, routers; this is the owner's call.
- **(a2) Upstream does not provide it.** TrustTunnel is in none of `upstream/stable`,
  `testing` or `unstable` (checked 2026-10-15); #3723 has no maintainer response.
- **(a3) Cheaper than the alternative.** The user's alternative is mihomo or husi. The cost
  here is new files plus marked blocks in `include/registry.go` (two registries, 6 lines)
  and `constant/proxy.go` (type constant and label, 4 lines); `go.mod` is not touched (§5).
- **(b) Isolation** — §5.
- **(c) Spec Kit** — feature [018](../../FEATURES/018-TRUSTTUNNEL/FEATURE.md) was written first.

## 2. Protocol (wire format, per `TrustTunnel/PROTOCOL.md` v1.0, the endpoint and the official client)

### 2.1 Transport

| Mode | How | ALPN |
|------|-----|------|
| HTTP/2 | TLS 1.2+ over TCP, SNI mandatory | `h2`; the official client rejects a session where anything else was negotiated |
| HTTP/3 | QUIC v1, no 0-RTT, no QUIC datagrams | `h3` |

The endpoint routes **only by SNI** (`main_hosts` lookup): a ClientHello without SNI or
with an unknown one is dropped at the TLS level, with no HTTP answer. `h2` and `h3` are
the only tunnel protocols; HTTP/1.1 is accepted for browsers and the subscription handler,
the tunnel client never uses it.

The 131072-byte initial stream window in `PROTOCOL.md` is the official client's tuning
(Chrome's constant), not a wire requirement; HTTP/2 SETTINGS travel inside TLS and are
visible only to the endpoint. The Go `http2` client uses its own 4 MiB window.

### 2.2 Authorization

`proxy-authorization: Basic base64(user:pass)` on **every** `CONNECT`, including `_udp2`
and `_check`. The endpoint keeps a map keyed by the standard base64 (with padding) of
`user:pass` and looks the header value up as a string: any deviation in the encoding is a
refusal with correct credentials. A refusal is 407 (`auth_failure_status_code`,
configurable to 403/404/405) with `proxy-authenticate: Basic realm=…`; the endpoint may
also close the session with GOAWAY / H3 connection error `0x1F` ("authentication
required"). Per the specification the client MUST close its session on 407 and
re-authenticate.

### 2.3 TCP

Plain `CONNECT` (not extended, no `:protocol`), `:authority` = `host:port`, IPv6 as
`[addr]:port`. A `CONNECT` whose authority has no port is an error on the endpoint. A domain
in `:authority` is resolved **by the endpoint**, with the endpoint's DNS.

Headers: `user-agent`, `proxy-authorization`. The user agent is `<platform> <app>` per the
specification; the official client's platform strings are `Windows`, `iOS`, `Mac`,
`Android`, `Linux`, and `<app>` is the name of the originating process (`chrome.exe`,
`unknown` when not known). The endpoint does not enforce the header (it is optional in its
parser) but logs it and passes it to a SOCKS5 upstream in the extended-auth mode, so it
is a client identity, not a liveness detail.

200 — the stream becomes a raw channel; otherwise 502 with `X-Warning: <code> - <text>`:

| `X-Warning` | Meaning | Extra |
|-------------|---------|-------|
| `300 - Connection failed for some reason` | I/O or other error (also a private destination with `allow_private_network_connections = false`) | — |
| `301 - …` | host unreachable | — |
| `302 - …` | connection timeout | — |
| `310 - …` | DNS answer is non-routable | `X-Adguard-Vpn-Error: <hostname>` |
| `311 - …` | DNS answer is loopback | `X-Adguard-Vpn-Error: <hostname>` |

### 2.4 UDP

One stream per session: `CONNECT` with `:authority: _udp2` (literally, without a port) and
`user-agent: <platform> _udp2`; frames on the stream, big-endian:

```
client → server:  u32 len | src_ip[16] | src_port u16 | dst_ip[16] | dst_port u16 | u8 L | app_name[L] | payload
server → client:  u32 len | src_ip[16] | src_port u16 | dst_ip[16] | dst_port u16 | payload
```

`len` does not include itself; IPv4 is 12 zero bytes + 4 address bytes, so an IPv6 address
inside `::/96` (including `::1`) cannot be carried: it decodes as IPv4, on the official
endpoint as well. Frames carry IP addresses only: the client resolves domains. `app_name` is the same application string as
in the user agent (the official client sends the originating process name). The endpoint
tracks flows per stream by the 4-tuple; the specification says 120 s idle, the endpoint's
default config is `udp_connections_timeout_secs = 300` — irrelevant to the client. A zero
`src` is acceptable (mihomo and sing-trusttunnel do this): with one `_udp2` stream per
sing-box UDP session, all datagrams of a session to one destination fall into one endpoint
flow, which is the intended mapping.

### 2.5 Health check

`CONNECT _check` with `user-agent: <platform> unknown` (observed on the stand with the
official client 1.1.11 over H2 and H3; the application slot is literally `unknown`); the
server answers 200 at once. In the
official client this is **not a timer**. It is issued on tunnel events — a new connection
opened, a connection migrating between upstreams, a flow-control update while the client is
waiting for data from the server, an ICMP request, wake-up after sleep (forced) — throttled
to one per `timeout` (connection timeout, default 30 s), with a response deadline of
`health_check_timeout` (default 7 s). Any data from the server cancels a pending check; a
failure starts recovery with backoff. The 7 s value is the deadline, not an interval.

### 2.6 Session limits on the endpoint

| Setting | Default | Effect on the client |
|---------|---------|----------------------|
| `client_listener_timeout_secs` | 600 | The endpoint closes a session that carried no request for 10 min (H2: closes the TLS connection; H3: QUIC `max_idle_timeout`) |
| `listen_protocols.http2.max_concurrent_streams` | 1000 | Streams per H2 session; the 1001st `CONNECT` waits or fails depending on the client |
| `listen_protocols.quic.initial_max_streams_bidi` | 4096 | Same for H3 |
| `allow_private_network_connections` | `false` | `CONNECT` to a private destination fails with 300/311 |
| `ipv6_available` | `true` | With `false`, IPv6 destinations fail |
| `disable_active_migration` | `true` | QUIC connection migration is off: a network change needs a new session |

The official client keeps an H3 session alive by sending ACK-eliciting packets every
`timeout` (30 s) while idle. On H2 it relies on the event-driven health check. Neither
is done here (§4, R3): the session is allowed to die while idle and is rebuilt on demand.

## 3. Config contract

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
  "tls": { "enabled": true, "server_name": "vpn.example.com" }
}
```

| Key | Type | Default | Validation |
|-----|------|---------|------------|
| `server`, `server_port` | `ServerOptions` | required | as for everyone; **`server` is an IP literal ⇒ `tls.server_name` is required** (the endpoint routes by SNI only, and Go sends no SNI for an IP; without this rule the symptom is a silent TLS drop) |
| `username`, `password` | `string` | required | empty — start error (husi checks only `username`; an empty password is rejected here on purpose, Q2) |
| `network` | `NetworkList` (`tcp`/`udp`) | both | as for everyone; `icmp` — start error with a clear message |
| `quic` | `bool` | `false` | `true` + `tls.utls.enabled` — start error |
| `quic_congestion_control` | `string` enum `bbr,cubic,reno` | `bbr` | unknown — start error (no silent fallback to BBR); `reno` maps to sing-quic `new_reno` |
| `health_check` | `bool` | `false` | — (semantics in R6, Q8) |
| `idle_timeout` | `Duration` | unset | **Proposal (Q7).** Session teardown after this long without a request; unset inherits a global default, `0` disables. Same key and semantics as the MASQUE outbound |
| `tls` | `OutboundTLSOptions` | required | `enabled: false` — `C.ErrTLSRequired`; ALPN, if set, must contain `h2` (H2) or `h3` (H3) |
| Dial Fields | `DialerOptions` | — | as for everyone |

The schema matches husi's `TrustTunnelOutboundOptions` (except `icmp` in `network` — out
of this task) plus `idle_timeout`. mihomo's keys (`health-check`, `udp`, `sni`, `alpn`,
`client-fingerprint`) are Clash YAML and play no part in the choice (Q2).

## 4. Requirements

- **R1. TCP.** `DialContext("tcp", dst)` opens `CONNECT dst` in the node's shared session and
  **returns after the server's answer**: 200 — a `net.Conn`; otherwise an error with the status
  code, `X-Warning` and, for 310/311, the hostname from `X-Adguard-Vpn-Error`. The call's
  `ctx` also bounds the wait for the answer. A domain destination goes into `:authority` as
  is, unresolved: the endpoint resolves it.
- **R2. UDP.** `ListenPacket` and `DialContext("udp")` open `CONNECT _udp2`; the destination
  domain is resolved via the node's dial fields (`domain_resolver` → DNS router); an empty
  resolve result is an error, not a panic; frames per §2.4; `WriteTo` to a domain without
  resolution is an error. The asymmetry with R1 (TCP domains resolved by the endpoint, UDP
  domains locally) is documented for the user; the recommended `domain_resolver` for the
  node is a DNS server detoured through the node itself.
- **R3. Session.** One session per node, opened lazily on the first request, outliving
  individual streams. H2 checks that `h2` was negotiated. H3 is QUIC v1 over a UDP socket
  from the dial fields (`detour` works).
  - **No keepalive.** No HTTP/2 PING (`ReadIdleTimeout` off), no QUIC `KeepAlivePeriod`; the
    QUIC session is allowed to time out silently (quic-go closes locally without sending).
    Its QUIC idle timeout is a fixed 600 s, the endpoint's own session limit; the idle
    window (`idle_timeout`) and the stale rule apply on top of it.
    While the node is idle there are no outgoing packets, whatever the config.
  - **Idle teardown.** After `idle_timeout` without a request the session is closed locally
    (no packets); the next request opens a new one. This is the MASQUE `idle_timeout`
    pattern.
  - **Transparent rebuild.** A dial that finds the session dead (closed by the endpoint,
    GOAWAY, QUIC timeout) or **stale** (older than the idle window but not yet torn down,
    e.g. the first dial after the device slept) rebuilds it and repeats the `CONNECT` once
    inside the same `ctx`; the caller sees either a `net.Conn` or the second attempt's
    error, never a spurious "dead node". A `CONNECT` on a stale session is bounded by a short
    budget (seconds, not the full `ctx`) so that a NAT-expired TCP connection does not hang
    the dial until the router's timeout.
- **R4. Lifecycle.** The constructor does not open the network; `Start(stage, scope)` /
  `Close()` follow the scoped lifecycle model (SPEC 117). `Close` closes the session and all
  streams.
- **R5. Network change.** `InterfaceUpdated()` resets the session (QUIC migration is off on
  the endpoint by default, §2.6).
- **R6. Health check.** With `health_check: true` — `CONNECT _check` **on events, not on a
  timer**, following the official client (§2.5): on a new dial when the session has been
  silent for longer than the throttle, on `InterfaceUpdated()`, on wake-up from pause;
  throttled to one per 30 s; response deadline 7 s; any data received from the server
  resets the throttle. A failed check tears the session down so that the dial that triggered
  it rebuilds (R3). While idle no check is sent (ENERGY). With `health_check: false` R3
  alone covers liveness. Whether the key earns its place at all is Q8.
- **R7. Authorization.** Basic per §2.2, standard base64 with padding. A 407 (or the
  configured refusal code) tears the session down (specification 5.2); GOAWAY / H3 error
  `0x1F` surfaces as "authentication required", not as a bare connection close.
- **R8. Without the tag.** Without `with_trusttunnel` both types are registered as stubs with
  the error `trusttunnel is not included in this build, rebuild with -tags with_trusttunnel`;
  the rest of the core is upstream byte for byte.
- **R9. Builds.** The tag is in `LX_TAGS` — desktop binaries and static router builds
  (owner's decision 2026-10-15, §7 Q4; enabled 2026-10-16): the `trusttunnel` type is in
  the release binaries. The AAR (`sharedTags`) does not carry it. The build without the tag
  and `go vet` under both tag sets are green.
- **R10. Identity headers.** `user-agent` per §2.3: platform from `GOOS`
  (`darwin` → `Mac`, `windows` → `Windows`, `linux` → `Linux`, `android` → `Android`,
  `ios` → `iOS`), application string per Q10; `<platform> _udp2` on the UDP stream,
  `<platform> unknown` on `_check` (as the official client does). The same application string goes into the `app_name` field of
  UDP frames. The originating process name from sniff metadata is **not** sent.
- **R11. Stream limit.** The node never opens a second session to get around the endpoint's
  `max_concurrent_streams` (§2.6): Go's `http2.Transport` does that by default and must be
  configured strict. A `CONNECT` beyond the limit waits for a free slot bounded by its `ctx`
  (proposal, Q9).
- **R12. Documentation.** A section in `docs-lx/protocols-transports.md` (as for MASQUE and
  chain), the release note, and the mapping table from the TrustTunnel export (FEATURE 018)
  are part of the deliverable.
- **R13. Fingerprint.** With H2 the ClientHello is what `tls` gives: uTLS with
  `tls.utls` (`fingerprint: chrome` is the recommended shape), Go's `crypto/tls` without it.
  The HTTP/2 preface is Chrome-like as far as `x/net/http2` allows without a fork
  (`newHTTP2Transport` in `client_http2.go`, pinned by `TestHTTP2Preface`): the SETTINGS
  values `HEADER_TABLE_SIZE 65536`, `ENABLE_PUSH 0`, `INITIAL_WINDOW_SIZE 6291456`,
  `MAX_HEADER_LIST_SIZE 262144` and the connection `WINDOW_UPDATE 15663105` are Chrome's.
  Go-like remain: the order of the settings (`2,4,5,6,1` instead of `1,2,4,6`), an extra
  `MAX_FRAME_SIZE 16384` entry (x/net always sends it; 16384 is the spec default), the
  pseudo-header order (hardcoded in x/net `internal/httpcommon`: a plain `CONNECT` carries
  `:authority` before `:method`, Chrome sends `:method` first) and HEADERS without priority.
  With H3 uTLS is unavailable (start error); the QUIC Initial, the ClientHello and the
  HTTP/3 SETTINGS are quic-go's. On both transports the inner TLS of a proxied HTTPS
  connection goes without padding, so TLS-in-TLS stays visible to traffic analysis.

## 5. Isolation and merge cost

Two layers, both in **upstream form** (owner 2026-10-15): new files are named and laid out
the way upstream lays out a tagged protocol (`tailscale`, `cloudflared`, `naive`), with no
`_lx` suffixes. The `// lx:` markers remain only on the lines inserted into shared upstream
files — they are the merge seam, not the shape of the code, and a submitter strips them.
This departs from the CHAIN pattern (SPEC 073) on purpose: chain is fork-only, this protocol
is meant to be liftable.

| What | Where | Upstream file? |
|------|-------|----------------|
| **Protocol core** — client and server: session, `CONNECT` streams, `_udp2` codec, `_check`, Basic auth, errors; H3 under `with_quic` inside the package | `transport/trusttunnel/` (Q11) — imports only `sagernet/sing`, `x/net/http2`, and in the `with_quic` files `sagernet/quic-go` and `sagernet/sing-quic`; **no** `adapter`, `option`, `log` of sing-box | no |
| Options — `TrustTunnelOutboundOptions`, `TrustTunnelInboundOptions` | `option/trusttunnel.go` | no |
| sing-box adapters — `outbound.go` (this task), `inbound.go` (task 134): dial fields, DNS, lifecycle, logging, mapping of options onto the core API | `protocol/trusttunnel/` | no |
| Registration / stub | `include/trusttunnel.go` (`//go:build with_trusttunnel`) + `include/trusttunnel_stub.go` | no |
| Registration calls | `include/registry.go` — `registerTrustTunnelInbound` / `registerTrustTunnelOutbound`, each inside `// lx:begin trusttunnel … // lx:end trusttunnel` | **yes, 6 lines** |
| `TypeTrustTunnel` constant and UI label | `constant/proxy.go` — the constant in the type block and the `case` in `ProxyDisplayName`, each behind `// lx:begin trusttunnel` | **yes, 4 lines** (§7, Q5) |
| Tags | `Makefile.lx` `LX_TAGS` | no (lx file) |
| Tests | `*_test.go` next to the code, no `_lx` suffix; stands in `lx-test/trusttunnel/` | no |

`include/quic.go` and `include/quic_stub.go` are not touched: the H3 halves of client and
server are gated by `with_quic` inside `transport/trusttunnel/` (`client_quic.go`,
`service_quic.go` and their stubs answering `ErrQUICNotIncluded`); `protocol/trusttunnel/`
needs no `with_quic` file of its own. User documentation stays in
`docs-lx/` (R12); `docs/configuration/*` pages are the submitter's job.

What a submitter takes upstream: `transport/trusttunnel/`, `option/trusttunnel.go`,
`protocol/trusttunnel/`, `include/trusttunnel*.go` as they are; the four marked lines in
`constant/proxy.go` and six in `include/registry.go` without the markers; plus docs and
`test/` suites written there.

## 6. Acceptance criteria

1. Builds without `with_trusttunnel`, with it, and the AAR (without the tag) are green;
   `go vet` under both sets.
2. Package unit tests: the `_udp2` frame codec (IPv4/IPv6, boundary lengths, truncated
   frame), parsing of `X-Warning` / `X-Adguard-Vpn-Error` errors, Basic auth with padding,
   config validation (§3, including "IP server without `server_name`"), the user-agent
   strings (R10).
3. A stand in `lx-test/`: the official `trusttunnel_endpoint` (Apache-2.0, release binary)
   in a container or locally, **with `allow_private_network_connections = true`** in
   `vpn.toml` (otherwise the stand's local targets get 300/311) and a self-signed
   certificate via `tls.certificate`; through the node — HTTP(S) over TCP, DNS over UDP, a
   large UDP frame; H2 and H3; a wrong password is an error from `DialContext`, not from
   the first `Read`, and the session is torn down after it.
4. Dialing via `detour` and as a `chain` link works.
5. While idle there are no outgoing packets, with `health_check` on and off (counters or
   pcap on the stand).
6. Stale session: after the endpoint's `client_listener_timeout_secs` (set to a few seconds
   on the stand) and after a simulated NAT drop (iptables DROP on the stand) the first dial
   succeeds within its `ctx`, with one rebuild and no error surfaced; `urltest` over the node
   shows no spurious failure.
7. A live server (own VPS or a public test one) — `curl` through the node on desktop.
8. Core package tests run without sing-box: client against the package's own server (task
   134) in-process, H2 and H3, plus the official endpoint on the stand (3). Upstream-style
   `test/` docker suites are **not** written here (owner 2026-10-15): the `test/` module does
   not build in the fork, and that work is left to whoever submits the protocol upstream.

## 7. Open questions (for discussion)

- **Q1. Implementation — decided by the owner 2026-10-15: in-tree, two layers.** The protocol
  core is a self-contained package with its own API (§5): client and server, no imports from
  sing-box `adapter`/`option`, written to the bar of a `sing-*` library so that it can be
  sent upstream by someone else or taken by husi/NekoBox directly; `protocol/trusttunnel/`
  holds only the sing-box adapters. Estimate ~700–900 lines for the client core and outbound
  adapter. Reused from the MASQUE outbound (in the adapter): the shared TLS client
  construction (`buildH2TLSClient`), the QUIC dial through dial fields, the session lifecycle
  and `idle_timeout` machinery. **Not** reused: `transport/masque` stream code — it is a
  hand-written framer for a single extended-`CONNECT` stream with capsules, while TrustTunnel
  multiplexes many plain `CONNECT` streams in one session, which is `x/net/http2`
  `ClientConn` and quic-go `http3.ClientConn` (plain `CONNECT` is supported there). Behaviour
  and versions are under control, R1/R3/R6 are covered from the start, no new modules in
  `go.mod`. The library `xchacha20-poly1305/sing-trusttunnel` v0.3.2 is not used:
  `Dial`/`ListenPacket` return on `httptrace.GotConn`, before the `CONNECT` answer, so a
  407/502 surfaces on the first `Read` (violates R1); its health check is a 7 s timer while
  `SetKeepIdleConnections(true)` (R6); it has no idle teardown or stale-session handling
  (R3); one maintainer, on sing-box 1.15-alpha. (It does check ALPN — the earlier claim in
  #46 that it does not was wrong.) Do not copy its code or the mihomo copy — the wire format
  is checked against `PROTOCOL.md`, the endpoint and the official client.
- **Q2. Key schema.** Matching husi (`quic` bool, like upstream `naive`) versus a separate
  `transport: h2|h3`. Proposal — husi's schema: NekoBox/husi configs move over unchanged.
  Two deliberate deviations to confirm: `idle_timeout` is added (Q7), and an empty
  `password` is rejected where husi accepts it.
- **Q3. `client_random` rules.** Needed only if servers with such rules show up (there are
  none by default). Facts: the official client enables the feature only on AdGuard-patched
  OpenSSL (`SSL_set_custom_client_random`), its MIPS builds lack it; `client_random_auth_key`
  derives the value from the SNI, so an endpoint with such a rule denies every connection
  without it at the TLS level — indistinguishable for the user from a wrong SNI. With H3 it
  is impossible by construction (standard `crypto/tls`); with H2 it needs the uTLS fork.
  Proposal — a separate task on a real request, H2 only.
- **Q4. Surfaces — decided by the owner 2026-10-15:** desktop and routers from the start
  (`LX_TAGS`). The AAR (LxBox) is not in this task.
- **Q5. `ProxyDisplayName` — decided by the owner 2026-10-15:** a marked `case` in
  `constant/proxy.go` (2 lines), the UI shows "TrustTunnel". By the same decision `chain`
  got the "Chain" label (SPEC 073 §8).
- **Q6. Inbound — decided by the owner 2026-10-15: written right away**, as task 134 of the
  same feature, on the server half of the core package. The model inside sing-box is the
  upstream `naive` inbound: an `http.Server` serving `CONNECT` with Basic auth over H2, H3
  attached through a hook set under `with_quic` (`ConfigureHTTP3ListenerFunc`). Scope and
  contract are in [SPEC 134](../134-TRUSTTUNNEL_INBOUND/SPEC.md); the question of how a
  failed upstream connect reaches the client (sing-box's router gives an inbound no dial
  result; `naive` answers 200 before routing) is settled there as a deferred 200 (SPEC 134
  R4, Q2).
- **Q7. `idle_timeout` and the stale-session budget.** Proposal: the key as in §3, the global
  default next to `lx.masque.idle_timeout` (a shared `lx.idle_timeout` or a sibling
  `lx.trusttunnel.idle_timeout`), default 5 min; the short `CONNECT` budget on a stale session
  3 s. Alternative: no key, a fixed window. Without any of this the first dial after a long
  idle hangs until the router's dial timeout (§2.6).
- **Q8. `health_check`: event-driven or dropped.** With R3 (transparent rebuild) the check
  adds little: it detects a dead session a round trip earlier than the `CONNECT` itself
  would. Options: keep the key with the event semantics of R6 (parses husi configs, costs
  nothing while idle), or accept the key and ignore it with a warning. Proposal — keep with
  R6 semantics.
- **Q9. Behaviour above `max_concurrent_streams`.** Wait for a free slot (R11), open a second
  session (what Go's transport does unasked, breaks "one session per node"), or fail the
  dial. Proposal — wait.
- **Q10. Application string in `user-agent` and `app_name`.** Options: `sing-box`,
  `sing-box-lx`, with or without the version (`sing-box-lx/1.14.3-lx.21`). The endpoint
  operator sees it in logs and in SOCKS5 extended auth. Proposal — `sing-box-lx` without the
  version. The core package takes the string as a parameter; the adapter sets it.
- **Q11. Where the core package lives.** Proposal — `transport/trusttunnel/` in this
  repository, next to `transport/masque`: no `go.mod` change, no submodule, the package is
  lifted into its own module later by copying. Alternative — a separate repository
  `sing-trusttunnel` under the owner's account as a module dependency; cleaner for upstream
  submission, but a fifth fork-submodule-like moving part on every sync. Either way the
  package boundary is the same. (The file layout around it is decided: upstream form, §5.)

## 8. Removal condition

Upstream sing-box adds a `trusttunnel` outbound in the surface the fork builds — this
implementation is removed in favour of the upstream one (§3.4), with a key migration if the
schemas diverge.

## 9. Sources

- Specification: `TrustTunnel/TrustTunnel` `PROTOCOL.md`, `CONFIGURATION.md`, `DEEP_LINK.md`
  (commit `48f424c`, 2026-10-09). Endpoint code checked for §2: `lib/src/http_downstream.rs`
  (status codes, `X-Warning`, authority parsing), `lib/src/tls_demultiplexer.rs` (SNI
  routing), `lib/src/authentication/registry_based.rs` (credential lookup),
  `lib/src/tunnel.rs` and `lib/src/quic_multiplexer.rs` (`client_listener_timeout`).
- Official client: `TrustTunnel/TrustTunnelClient` (commit `64e2e5b`, 2026-10-15):
  `core/src/utils.cpp` (user agent, credentials), `core/src/tunnel.cpp` (health-check
  triggers and throttle), `core/src/http2_upstream.cpp` / `http3_upstream.cpp` (ALPN check,
  window, idle keepalive), `net/include/net/utils.h` (anti-DPI: 1-byte split, 25 ms),
  `net/include/net/tls_client_random_auth_key.h` (`client_random` gating).
- Third-party: mihomo `transport/trusttunnel` (a copy of sing-trusttunnel v0.1.1),
  husi `libcore/plugin/trusttunnel`, `xchacha20-poly1305/sing-trusttunnel` v0.3.2.
