# SPEC 133 — `trusttunnel` outbound: TCP + UDP over HTTP/2 and HTTP/3

> 🌐 Русская версия: **[SPEC.ru.md](SPEC.ru.md)**.
>
> **Language.** This feature came from outside (PR [#45](https://github.com/Leadaxe/sing-box-lx/pull/45), discussion in [#46](https://github.com/Leadaxe/sing-box-lx/issues/46)), so its documents use English as the primary language. The Russian copy (`*.ru.md`) sits next to each file to make the feature easier to maintain.

**Feature:** [TRUSTTUNNEL](../../FEATURES/018-TRUSTTUNNEL/FEATURE.md)

| Field | Value |
|-------|-------|
| Type | F (feature) — client outbound |
| Status | N (new) — draft for discussion; Q1 (in-tree implementation), Q4 (desktop + routers) and Q5 ("TrustTunnel" label) are decided, the rest of §7 is open |
| Branch | `lx` |
| Build tag | `with_trusttunnel` |
| Discussion | [Leadaxe/sing-box-lx#46](https://github.com/Leadaxe/sing-box-lx/issues/46) |
| Origin | PR [Leadaxe/sing-box-lx#45](https://github.com/Leadaxe/sing-box-lx/pull/45) (closed: three protocols without a task, the build was broken); owner 2026-10-15: start with TrustTunnel |
| Related | [MASQUE_WARP](../../FEATURES/009-MASQUE_WARP/FEATURE.md) (in-tree H2/H3 `CONNECT` machinery) · [ENERGY](../../FEATURES/008-ENERGY/FEATURE.md) (health check while idle) · [CHAIN](../../FEATURES/015-CHAIN/FEATURE.md) (node as a link) · [UPSTREAM_SYNC](../../FEATURES/005-UPSTREAM_SYNC/FEATURE.md) (removal condition) |

## 0. TL;DR

A new `trusttunnel` outbound behind the `with_trusttunnel` tag: TCP as `CONNECT host:port`,
UDP as `CONNECT _udp2` with the protocol's frames, transport HTTP/2 over TLS or HTTP/3
over QUIC (`quic: true`). The key schema is the one already used by husi and its
derivatives. Inbound, ICMP, `client_random` rules and `auto` H2/H3 selection are out of
the task. The implementation is in-tree, on top of the MASQUE H2/H3 code, without a
third-party library (§7, Q1).

## 1. Problem and §3.1 criteria

- **(a1) Needed.** The request came from a downstream client (NekoBox/NyameBox is building
  its next version on sing-box-lx and already carries the protocol in its fork — PR #45).
  Upstream has an open request, SagerNet/sing-box#3723 (January 2026, ~30 reactions, field
  reports: works in Russia, unstable in Iran). There is **no request yet** from the priority
  consumers — the launcher, LxBox, routers; this is the owner's call.
- **(a2) Upstream does not provide it.** TrustTunnel is in none of `upstream/stable`,
  `testing` or `unstable` (checked 2026-10-15); #3723 has no maintainer response.
- **(a3) Cheaper than the alternative.** The user's alternative is mihomo or husi. The cost
  here is new files plus marked blocks in `include/registry.go` (3 lines) and
  `constant/proxy.go` (2 lines); `go.mod` is not touched (§5).
- **(b) Isolation** — §5.
- **(c) Spec Kit** — feature [018](../../FEATURES/018-TRUSTTUNNEL/FEATURE.md) was written first.

## 2. Protocol (wire format, per `TrustTunnel/PROTOCOL.md` v1.0 and the official client)

### 2.1 Transport

| Mode | How | ALPN |
|------|-----|------|
| HTTP/2 | TLS over TCP; initial stream window 131072 | `h2` (the client offers `h2,http/1.1` but drops anything other than `h2`) |
| HTTP/3 | QUIC v1, no 0-RTT, no QUIC datagrams | `h3` |

The server routes by SNI and drops an unknown SNI at the TLS level. The server accepts
HTTP/1.1 only for browsers; the tunnel client does not use it.

### 2.2 Authorization

`proxy-authorization: Basic base64(user:pass)` on **every** `CONNECT`, including `_udp2`
and `_check`. The server compares the base64 string byte for byte with the standard
encoding **with padding**. A refusal is 407 (configurable to 403/404/405) with
`proxy-authenticate`.

### 2.3 TCP

Plain `CONNECT` (not extended, no `:protocol`), `:authority` = `host:port`, IPv6 as
`[addr]:port`. Headers: `user-agent: <platform> <app>`, `proxy-authorization`. 200 — the
stream becomes a raw channel; otherwise usually 502 with `X-Warning: <code> - <text>`:
300 connect failed, 301 host unreachable, 302 timeout, 310/311 DNS non-routable / loopback.

### 2.4 UDP

One stream per session: `CONNECT` with `:authority: _udp2` (literally, without a port),
frames on the stream, big-endian:

```
client → server:  u32 len | src_ip[16] | src_port u16 | dst_ip[16] | dst_port u16 | u8 L | app_name[L] | payload
server → client:  u32 len | src_ip[16] | src_port u16 | dst_ip[16] | dst_port u16 | payload
```

`len` does not include itself; IPv4 is 12 zero bytes + 4 address bytes. Frames carry IP
addresses only: the client resolves domains. The server tracks flows by the 4-tuple, idle
120 s per the specification. A zero `src` is acceptable (mihomo does this).

### 2.5 Health check

`CONNECT _check`, the server answers 200 at once. The official client sends it on tunnel
events, no more often than `timeout`, with a 7 s deadline; any data from the server cancels
a pending check; a failure starts reconnection with backoff.

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
  "tls": { "enabled": true, "server_name": "vpn.example.com" }
}
```

| Key | Type | Default | Validation |
|-----|------|---------|------------|
| `server`, `server_port` | `ServerOptions` | required | as for everyone |
| `username`, `password` | `string` | required | empty — start error |
| `network` | `NetworkList` (`tcp`/`udp`) | both | as for everyone |
| `quic` | `bool` | `false` | `true` + `tls.utls.enabled` — start error |
| `quic_congestion_control` | `string` enum `bbr,cubic,reno` | `bbr` | unknown — start error (no silent fallback to BBR) |
| `health_check` | `bool` | `false` | — |
| `tls` | `OutboundTLSOptions` | required | `enabled: false` — `C.ErrTLSRequired`; ALPN, if set, must contain `h2` (H2) or `h3` (H3) |
| Dial Fields | `DialerOptions` | — | as for everyone |

The schema matches husi's `TrustTunnelOutboundOptions` (except `icmp` in `network` — out
of this task; the value `icmp` is a start error with a clear message).

## 4. Requirements

- **R1. TCP.** `DialContext("tcp", dst)` opens `CONNECT dst` in the node's shared session and
  **returns after the server's answer**: 200 — a `net.Conn`; otherwise an error with the code
  and `X-Warning`. The call's `ctx` also bounds the wait for the answer.
- **R2. UDP.** `ListenPacket` and `DialContext("udp")` open `CONNECT _udp2`; the destination
  domain is resolved via the node's dial fields (`domain_resolver` → DNS router); an empty
  resolve result is an error, not a panic; frames per §2.4; `WriteTo` to a domain without
  resolution is an error.
- **R3. Transport.** One session per node, opened lazily on the first request, outliving
  individual streams; after a drop it is recreated by the next request. H2 checks that `h2`
  was negotiated. H3 is QUIC v1 over a UDP socket from the dial fields (`detour` works).
- **R4. Lifecycle.** The constructor does not open the network; `Start(stage, scope)` /
  `Close()` follow the scoped lifecycle model (SPEC 117). `Close` closes the session and all
  streams.
- **R5. Network change.** `InterfaceUpdated()` resets the session.
- **R6. Health check.** With `health_check: true` — `CONNECT _check` at most once per 7 s,
  only while the node has live streams or had a request in the last 60 s; while idle the
  timer is stopped. A failed check resets the session.
- **R7. Authorization.** Basic per §2.2, standard base64 with padding.
- **R8. Without the tag.** Without `with_trusttunnel` the type is registered as a stub with
  the error `trusttunnel is not included in this build, rebuild with -tags with_trusttunnel`;
  the rest of the core is upstream byte for byte.
- **R9. Builds.** The tag goes into `LX_TAGS` — desktop binaries and static router builds
  (owner's decision 2026-10-15, §7 Q4). The AAR (`sharedTags`) does not carry it. The build
  without the tag and `go vet` under both tag sets are green.

## 5. Isolation and merge cost

Following the CHAIN pattern (SPEC 073), not MASQUE:

| What | Where | Upstream file? |
|------|-------|----------------|
| `TypeTrustTunnel` constant | `constant/trusttunnel_lx.go` | no |
| Options | `option/trusttunnel_lx.go` | no |
| Registration / stub | `include/lx_trusttunnel.go` + `include/lx_trusttunnel_stub.go` | no |
| Registration call | `include/registry.go` — block `// lx:begin trusttunnel … // lx:end trusttunnel` | **yes, 3 lines** |
| Protocol | `protocol/trusttunnel/` (H3 part under `with_quic` inside the package) | no |
| UI label | `constant/proxy.go` `ProxyDisplayName` — `case` behind `// lx:begin trusttunnel` | **yes, 2 lines** (§7, Q5) |
| Tags | `Makefile.lx` `LX_TAGS` | no (lx file) |

`include/quic.go` and `include/quic_stub.go` are not touched.

## 6. Acceptance criteria

1. Builds without `with_trusttunnel`, with it, and the AAR (without the tag) are green;
   `go vet` under both sets.
2. Package unit tests: the `_udp2` frame codec (IPv4/IPv6, boundary lengths, truncated
   frame), parsing of `X-Warning` errors, Basic auth with padding, config validation (§3).
3. A stand in `lx-test/`: the official `trusttunnel_endpoint` (Apache-2.0, release binary)
   in a container or locally; through the node — HTTP(S) over TCP, DNS over UDP, a large UDP
   frame; H2 and H3; a wrong password is an error from `DialContext`, not from the first `Read`.
4. Dialing via `detour` and as a `chain` link works.
5. While idle with `health_check: true` there are no outgoing packets (observed via counters
   or pcap on the stand).
6. A live server (own VPS or a public test one) — `curl` through the node on desktop.

## 7. Open questions (for discussion)

- **Q1. Implementation — decided by the owner 2026-10-15: in-tree**, in `protocol/trusttunnel/`
  (~700–900 lines, estimate) on top of the MASQUE H2/H3 `CONNECT` code. Behaviour and versions
  are under control, R1/R6 are covered from the start, no new modules in `go.mod`. The library
  `xchacha20-poly1305/sing-trusttunnel` v0.3.2 is not used: `Dial`/`ListenPacket` do not wait
  for the `CONNECT` answer (violates R1), the health check ticks while idle (R6), ALPN is not
  checked; one maintainer, on sing-box 1.15-alpha. Do not copy its code or the mihomo copy —
  the wire format is checked against `PROTOCOL.md` and the official client.
- **Q2. Key schema.** Matching husi (`quic` bool, like upstream `naive`) versus a separate
  `transport: h2|h3`. Proposal — husi's schema: NekoBox/husi configs move over unchanged.
- **Q3. `client_random` rules.** Needed only if servers with such rules show up (there are
  none by default). Proposal — a separate task on a real request, via the uTLS fork.
- **Q4. Surfaces — decided by the owner 2026-10-15:** desktop and routers from the start
  (`LX_TAGS`). The AAR (LxBox) is not in this task.
- **Q5. `ProxyDisplayName` — decided by the owner 2026-10-15:** a marked `case` in
  `constant/proxy.go` (2 lines), the UI shows "TrustTunnel". By the same decision `chain`
  got the "Chain" label (SPEC 073 §8).
- **Q6. Inbound.** Out of this task; whether to open a second task right away or on request.

## 8. Removal condition

Upstream sing-box adds a `trusttunnel` outbound in the surface the fork builds — this
implementation is removed in favour of the upstream one (§3.4), with a key migration if the
schemas diverge.

## 9. Sources

- Specification: `TrustTunnel/TrustTunnel` `PROTOCOL.md`, `CONFIGURATION.md`, `DEEP_LINK.md`
  (commit `48f424c`, 2026-10-09).
- Official client: `TrustTunnel/TrustTunnelClient` (commit `64e2e5b`, 2026-10-15).
- Third-party: mihomo `transport/trusttunnel` (a copy of sing-trusttunnel v0.1.1),
  husi `libcore/plugin/trusttunnel`, `xchacha20-poly1305/sing-trusttunnel` v0.3.2.
