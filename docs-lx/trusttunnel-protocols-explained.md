# TrustTunnel: how it works and how sing-box-lx supports it

> 🌐 Русская версия: **[trusttunnel-protocols-explained.ru.md](trusttunnel-protocols-explained.ru.md)**.

> 🧭 **Where to look.** The fork's documentation has three levels, by the reader's question:
> [lx-config](lx-config.md): what the fork has and how to enable it;
> [protocols-transports](protocols-transports.md): every field, type, default, error text;
> [xray-protocols-explained](xray-protocols-explained.md), [amneziawg-explained](amneziawg-explained.md)
> and [trusttunnel-protocols-explained](trusttunnel-protocols-explained.md):
> how it works, why, how the fork does it and how it differs from vanilla.

TrustTunnel is the AdGuard VPN protocol, open-sourced in November 2025
under the Apache-2.0 licence. From the outside it is ordinary HTTPS to a
real domain: HTTP/2 over TLS or HTTP/3 over QUIC. Inside there is a proxy
`CONNECT` with Basic authorization for every TCP connection, and a separate
stream for UDP. Vanilla sing-box does not support the protocol; the request
[SagerNet/sing-box#3723](https://github.com/SagerNet/sing-box/issues/3723)
has been open since January 2026 with no maintainer response. `sing-box-lx`
carries both halves: an outbound that connects to a server, and an inbound
that turns sing-box itself into a server for the official clients.

This document answers three questions. **How the protocol works**: what it
does to the bytes on the wire, where it sits among its neighbours and what
an intermediary can see. **How the fork implements it**: where the code
lives, which decisions were made and what has been verified. **How to use
it**: which transport to pick, what a server needs and where DNS behaves in
a non-obvious way. Fields, defaults and error texts are not listed here.
For those, see
[protocols-transports §6](protocols-transports.md#6-trusttunnel-outbound-and-inbound)
(parameter reference and key-mapping tables). The current state of the
area is in the feature spec
[018-TRUSTTUNNEL](../SPECS/FEATURES/018-TRUSTTUNNEL/FEATURE.md).

Audience: a user who has a TrustTunnel node or a `tt://` link, and an
operator deciding whether to run a server on the official endpoint or on
sing-box. TLS knowledge at the level of "there is a ClientHello, there is a
certificate" and HTTP knowledge at the level of "there is a method and
there are headers" is assumed.

Callouts in the text are marked by type:

- ⚠️ pitfall: something that fails silently and does not explain itself;
- 🔀 differs from the official implementation;
- 🧭 TL;DR: the section's conclusion in a few sentences;
- 📖 normative source.

> The `trusttunnel` type ships in the desktop and router release binaries
> since lx.21. The Android AAR does not carry it
> ([§5.1](#51-status-and-builds)).

## Table of contents

- [§0 What TrustTunnel is](#0-what-trusttunnel-is)
  - [0.1 The project and its parts](#01-the-project-and-its-parts)
  - [0.2 A proxy, not a VPN](#02-a-proxy-not-a-vpn)
  - [0.3 A protocol without AdGuard's network](#03-a-protocol-without-adguards-network)
- [§1 What it looks like on the wire](#1-what-it-looks-like-on-the-wire)
  - [1.1 Transport: HTTP/2 or HTTP/3](#11-transport-http2-or-http3)
  - [1.2 One session, many streams](#12-one-session-many-streams)
  - [1.3 TCP: a plain CONNECT](#13-tcp-a-plain-connect)
  - [1.4 UDP: the _udp2 stream and its frames](#14-udp-the-_udp2-stream-and-its-frames)
  - [1.5 The service streams _check and _icmp](#15-the-service-streams-_check-and-_icmp)
  - [1.6 Authorization and server answers](#16-authorization-and-server-answers)
  - [1.7 user-agent: who is knocking](#17-user-agent-who-is-knocking)
  - [1.8 The server: SNI routing and limits](#18-the-server-sni-routing-and-limits)
- [§2 Where it sits among protocols](#2-where-it-sits-among-protocols)
- [§3 Detectability: what an intermediary sees](#3-detectability-what-an-intermediary-sees)
  - [3.1 TLS-in-TLS without padding](#31-tls-in-tls-without-padding)
  - [3.2 Fingerprints](#32-fingerprints)
  - [3.3 Active probing of the server](#33-active-probing-of-the-server)
  - [3.4 client_random rules](#34-client_random-rules)
- [§4 The session model](#4-the-session-model)
  - [4.1 How the official client keeps its session](#41-how-the-official-client-keeps-its-session)
  - [4.2 How our node keeps its session](#42-how-our-node-keeps-its-session)
  - [4.3 Three approaches side by side](#43-three-approaches-side-by-side)
  - [4.4 What this means for the battery and for groups](#44-what-this-means-for-the-battery-and-for-groups)
- [§5 The implementation in sing-box-lx](#5-the-implementation-in-sing-box-lx)
  - [5.1 Status and builds](#51-status-and-builds)
  - [5.2 Two layers](#52-two-layers)
  - [5.3 Outbound](#53-outbound)
  - [5.4 Inbound](#54-inbound)
  - [5.5 What was verified on the stand](#55-what-was-verified-on-the-stand)
  - [5.6 What is not supported](#56-what-is-not-supported)
  - [5.7 Examples](#57-examples)
- [§6 Practical guidance](#6-practical-guidance)
  - [6.1 HTTP/2 or HTTP/3](#61-http2-or-http3)
  - [6.2 SNI and the certificate](#62-sni-and-the-certificate)
  - [6.3 Private networks on the inbound](#63-private-networks-on-the-inbound)
  - [6.4 DNS for UDP: a resolver through the node](#64-dns-for-udp-a-resolver-through-the-node)
  - [6.5 Typical failures](#65-typical-failures)
- [§7 Glossary](#7-glossary)
- [See also](#see-also)

---

# 0. What TrustTunnel is

## 0.1 The project and its parts

The protocol was developed at AdGuard VPN and is published by the
[TrustTunnel](https://github.com/TrustTunnel) organization on GitHub.
Everything in it is open under Apache-2.0:

| Part | Repository | What it is |
|---|---|---|
| Specification | `TrustTunnel/TrustTunnel`, file `PROTOCOL.md` | Version 1.0 of December 2025: transport, streams, frame formats, errors |
| Endpoint | `TrustTunnel/TrustTunnel` | A Rust server (`trusttunnel_endpoint`) for Linux and macOS, with a setup wizard and export of client configs and `tt://` links |
| Client libraries and CLI | `TrustTunnel/TrustTunnelClient` | C++ libraries and the CLI client `trusttunnel_client` for Linux, macOS and Windows; runs as a system-wide tunnel over TUN or as a SOCKS5 proxy |
| App | `TrustTunnel/TrustTunnelFlutterClient` | A Flutter GUI for iOS and Android, in the App Store and Google Play |

Third-party implementations already exist around the official parts:
mihomo, husi, Shadowrocket and Surge support the protocol, and there are
the Go library `sing-trusttunnel` and the sing-box-extended fork. The
normative source for the wire format is `PROTOCOL.md`, the endpoint code
and the official client, not the third-party implementations.

> 📖 Specification: `PROTOCOL.md` in
> [TrustTunnel/TrustTunnel](https://github.com/TrustTunnel/TrustTunnel);
> server settings: `CONFIGURATION.md` in the same repository; client
> settings: the CLI client README in
> [TrustTunnel/TrustTunnelClient](https://github.com/TrustTunnel/TrustTunnelClient).

## 0.2 A proxy, not a VPN

The official apps present TrustTunnel as a VPN: they bring up a TUN,
change routes and the system DNS, and keep a kill switch. The protocol
itself, however, works at the level of connections, not IP packets. Each
TCP connection of an application becomes a separate request "connect me to
`host:port`", and each UDP datagram travels in a frame with addresses
inside. The server gives the client no address, accepts no IP packets from
it and never dials towards it. The TUN in the official client is local
plumbing that assembles connections from packets, which is what sing-box
does as well.

> 🧭 **TL;DR:** TrustTunnel is closer to NaïveProxy and HTTPS proxies than
> to WireGuard. The difference between a proxy and a VPN is covered in the
> [xray-protocols-explained disclaimer](xray-protocols-explained.md#disclaimer-how-a-proxy-differs-from-a-vpn).

| | TrustTunnel | WireGuard, AmneziaWG, MASQUE CONNECT-IP |
|---|---|---|
| What travels in the tunnel | a TCP stream or a UDP datagram with its destination address | an IP packet as is |
| Who resolves the domain of a TCP destination | the server | the client, before sending the packet |
| ICMP | a separate `_icmp` stream, echo only | like any IP packet |
| Client address inside the tunnel | none | yes |
| Place in sing-box | outbound and inbound | endpoint |

This is also why the sing-box form was chosen: `trusttunnel` is an
outbound and an inbound, like `naive` and `anytls`, not an endpoint like
WireGuard or the upstream `masque-server`, which terminate IP packets
([SPEC 134 §0](../SPECS/TASKS/134-TRUSTTUNNEL_INBOUND/SPEC.md)).

## 0.3 A protocol without AdGuard's network

AdGuard stresses that TrustTunnel is independent of the AdGuard VPN
service. What is open is the protocol and its implementation, not access to
AdGuard's servers: there is no free way into AdGuard's network through a
third-party client, and nothing here works the way WARP is open to `wgcf`.
A TrustTunnel node for sing-box-lx is a server of your own: the official
endpoint on your machine, a third-party provider's server, or our inbound
([§5.4](#54-inbound)).

---

# 1. What it looks like on the wire

## 1.1 Transport: HTTP/2 or HTTP/3

| Mode | How | ALPN |
|---|---|---|
| HTTP/2 | TLS 1.2+ over TCP, SNI mandatory | `h2`; the official client drops a session where anything else was negotiated |
| HTTP/3 | QUIC v1, no 0-RTT, no QUIC datagrams | `h3` |

There are no other tunnel protocols. The endpoint accepts HTTP/1.1 for
browsers and for serving a subscription; the tunnel client never uses it.
The specification requires the client to verify the server certificate
and to send SNI.

The official client has an `auto` mode: it tries HTTP/3 first, starts
HTTP/2 in parallel after one second, and keeps whichever session opens
first. For such a client, a server that offers only one of the two is
normal.

> ⚠️ The 131072-byte stream window in `PROTOCOL.md` is the official
> client's tuning (Chrome's constant), not a protocol requirement. HTTP/2
> SETTINGS travel inside TLS and are visible only to the server.

## 1.2 One session, many streams

A **session** is one HTTP/2 or HTTP/3 connection between client and
server. Inside it live **streams**, one per task:

```
client                                                  endpoint
  │                                                        │
  │══ TLS + HTTP/2 (or QUIC + HTTP/3), one session ════════│
  │                                                        │
  │── stream 1: CONNECT example.com:443 ─► 200 ── bytes ──►│──► example.com
  │── stream 3: CONNECT 192.0.2.7:22    ─► 200 ── bytes ──►│──► 192.0.2.7
  │── stream 5: CONNECT _udp2           ─► 200 ── frames ─►│──► any UDP destinations
  │── stream 7: CONNECT _check          ─► 200, closed     │
```

Every TCP connection has its own stream. All UDP of a session goes into
one `_udp2` stream, with datagrams separated by frames that carry
addresses. ICMP works the same way as UDP, in an `_icmp` stream.

How many streams one session may hold is up to the server: the official
endpoint defaults to 1000 on HTTP/2 and 4096 on HTTP/3
([§1.8](#18-the-server-sni-routing-and-limits)).

## 1.3 TCP: a plain CONNECT

For TCP the client sends a plain `CONNECT` (not extended, without the
`:protocol` pseudo-header):

```
:method: CONNECT
:authority: example.com:443
user-agent: <platform> <application>
proxy-authorization: Basic <base64(user:pass)>
```

`:authority` is `host:port`, with IPv6 in square brackets
(`[2001:db8::1]:443`). The port is mandatory: a `CONNECT` without a port is
a request error on the endpoint. A domain in `:authority` goes as is and is
resolved **on the server**, with its DNS.

A 200 answer turns the stream into a raw bidirectional channel to the
target. When it arrives matters: the official endpoint answers 200 only
**after** the connection to the target is established. The client
therefore learns about a failed connect right from the answer, not on the
first read. Any other code is a refusal; details are in
[§1.6](#16-authorization-and-server-answers).

Closing the stream closes the connection: `END_STREAM` or `FIN` for a
graceful close, `RST_STREAM` or `RESET_STREAM` for an abrupt one.

## 1.4 UDP: the _udp2 stream and its frames

UDP is opened with one stream per session:

```
:method: CONNECT
:authority: _udp2
user-agent: <platform> _udp2
proxy-authorization: Basic <base64(user:pass)>
```

`_udp2` is written literally, without a port. The server answers 200 at
once, and from then on frames travel both ways on the stream. All integers
are big-endian:

```
client → server:
  ┌──────────┬─────────────┬──────────┬─────────────┬──────────┬──────┬──────────┬──────────┐
  │ len u32  │ src_ip [16] │ src_port │ dst_ip [16] │ dst_port │ L u8 │ app [L]  │ payload  │
  └──────────┴─────────────┴──────────┴─────────────┴──────────┴──────┴──────────┴──────────┘

server → client:
  ┌──────────┬─────────────┬──────────┬─────────────┬──────────┬──────────┐
  │ len u32  │ src_ip [16] │ src_port │ dst_ip [16] │ dst_port │ payload  │
  └──────────┴─────────────┴──────────┴─────────────┴──────────┴──────────┘
```

- `len` does not count itself.
- An address is always 16 bytes. IPv4 is encoded as 12 zero bytes followed
  by the 4 address bytes.
- `app` is the application string, the same one as in the `user-agent` of
  TCP requests; the official client puts the originating process name
  there. Reply frames do not have this field.
- Frames carry IP addresses only, no domains: the client resolves a UDP
  destination itself.

The server tracks UDP "connections" by the 4-tuple of source, source port,
destination and destination port within the stream, and forgets them after
inactivity: 120 s per the specification, 300 s in the endpoint's default
config. A zero source is acceptable: mihomo and our outbound send one, and
then all datagrams of one sing-box UDP session to one destination fall into
one flow on the server.

> ⚠️ **IPv6 inside `::/96` cannot be carried over UDP.** In the frame
> format, 12 zero bytes mean IPv4, so an address of the form `::a.b.c.d`,
> including `::1`, decodes as IPv4. The specification makes an exception
> for `::1`, but the official endpoint decodes such addresses the same way
> our code does. Public addresses never hit this; loopback stands do.

## 1.5 The service streams _check and _icmp

`CONNECT _check` asks whether the session is alive. A server with valid
authorization answers 200 at once and closes the stream; 407 means an
authorization refusal, and anything else that the session is probably
unhealthy. When and why a client sends `_check` is covered in
[§4](#4-the-session-model): this is the main place where implementations
differ.

`CONNECT _icmp` opens a stream for ICMP echo. Its frames carry an
identifier, the destination address, a sequence number and the TTL, but
not the request body: the data size is sent as a number. The server's
reply carries the identifier, the responder's address, the ICMP type and
code and the sequence number. This tunnels `ping` and nothing else. On the
official endpoint ICMP is enabled by a separate `[icmp]` section and needs
superuser privileges; we have it neither in the outbound nor in the inbound
([§5.6](#56-what-is-not-supported)).

## 1.6 Authorization and server answers

Every `CONNECT`, including `_udp2` and `_check`, carries
`proxy-authorization: Basic base64(user:pass)`. There is no separate
handshake: the credentials travel in every request, and the outer TLS is
the only thing that keeps them confidential.

> ⚠️ The official endpoint compares the header value **as a string** with
> the standard base64 **with padding**. Any other encoding of the same
> credentials is a refusal with the correct password. Our outbound encodes
> in the standard way; our inbound parses the header and compares the
> pair, so it also accepts a non-standard encoding.

Answers:

| Answer | When | Also in the answer |
|---|---|---|
| 200 | target connected (TCP); at once for `_udp2` and `_check` | — |
| 407 | wrong credentials | `proxy-authenticate: Basic realm=…`; the code can be configured to 403/404/405 |
| 502 + `X-Warning: 300 - …` | I/O or other error; also a private destination when private networks are refused | — |
| 502 + `X-Warning: 301 - …` | host unreachable | — |
| 502 + `X-Warning: 302 - …` | connection timeout | — |
| 502 + `X-Warning: 310 - …` | the server's DNS returned a non-routable address | `X-Adguard-Vpn-Error: <hostname>` |
| 502 + `X-Warning: 311 - …` | the server's DNS returned loopback | `X-Adguard-Vpn-Error: <hostname>` |

Per the specification, a client that gets 407 must close its session and
authenticate again. The server can also refuse at the session level: a
GOAWAY on HTTP/2 or an HTTP/3 connection error with code `0x1F`,
"authentication required".

## 1.7 user-agent: who is knocking

The specification sets `user-agent: <platform> <application>`. The
official client writes the platform as one of `Windows`, `iOS`, `Mac`,
`Android`, `Linux`, and the application as the name of the process that
opened the connection (`chrome.exe`, or `unknown` when it is not known). On
the stand, client 1.1.11 on macOS sent `Mac trusttunnel_client` on
`CONNECT host:port` and `Mac unknown` on `CONNECT _check`. The `_udp2`
stream gets `<platform> _udp2`, the `_icmp` stream `<platform> _icmp`.

The endpoint does not require the header, but it logs it and passes it to
a SOCKS5 upstream in the extended-auth mode. It is a client identity that
the operator sees, not a technical detail.

> 🔀 Our outbound introduces itself as `<platform> sing-box-lx`, with the
> platform taken from the build's OS, and puts the same string
> `sing-box-lx` into the `app` field of every UDP frame. The originating
> process name is never sent, even when sing-box knows it.

## 1.8 The server: SNI routing and limits

The official endpoint picks a handler **by SNI only**. `hosts.toml` has the
main tunnel hosts (`main_hosts`) and optional hosts for reachability
checks, speed tests and a reverse proxy. A ClientHello without SNI or with
an unknown one is dropped at the TLS level, without any HTTP answer.

The defaults a client feels:

| Endpoint setting | Default | What the client sees |
|---|---|---|
| `client_listener_timeout_secs` | 600 | A session with no request for 10 minutes is closed: HTTP/2 by closing the TLS connection, HTTP/3 by the QUIC idle timeout |
| `listen_protocols.http2.max_concurrent_streams` | 1000 | Streams per HTTP/2 session |
| `listen_protocols.quic.initial_max_streams_bidi` | 4096 | The same for HTTP/3 |
| `allow_private_network_connections` | `false` | A `CONNECT` to a private address gets 502 with code 300 or 311 |
| `ipv6_available` | `true` | With `false`, IPv6 destinations are refused |
| `disable_active_migration` | `true` | QUIC migration is off: a network change needs a new session |
| `udp_connections_timeout_secs` | 300 | UDP flow inactivity on the server; irrelevant to the client |

---

# 2. Where it sits among protocols

In essence TrustTunnel is an HTTPS proxy with `CONNECT`, packaged to look
like ordinary HTTPS to a website from the outside. Its closest relative is
NaïveProxy: the same `CONNECT` over HTTP/2 or HTTP/3 with Basic
authorization. The table puts it next to protocols that solve a similar
problem by other means. The rows about the neighbours are a short summary
for comparison; each protocol has its own details.

| Protocol | Layer | Transport | Multiplexing | UDP | Padding | Auth | What a prober sees |
|---|---|---|---|---|---|---|---|
| **TrustTunnel** | L4, connections | TLS + HTTP/2 or QUIC + HTTP/3 | all connections of a node are streams of one session | `_udp2` stream, frames with IP addresses | none | Basic on every `CONNECT` | official endpoint: unknown SNI is a TLS drop, the right one gets 407 with `proxy-authenticate` (by default); our inbound: an empty 404 to everything except `CONNECT` |
| **NaïveProxy** | L4 | TLS + HTTP/2 or QUIC, Chromium's network stack | streams of one session | none of its own; `udp_over_tcp` in sing-box | yes | Basic in `CONNECT` | an ordinary web server in front of the proxy |
| **HTTPS proxy with CONNECT** | L4 | TLS + HTTP/1.1 or HTTP/2 | depends on the implementation | none | none | Basic | a proxy that says so itself |
| **Trojan** | L4 | TLS over TCP | none, one connection per connection | its own command inside the TLS stream | none | password hash at the start of the stream | on a wrong password, a hand-off to a real web server (`fallback`) |
| **Hysteria2, TUIC** | L4 | QUIC with its own congestion control | QUIC streams | QUIC datagrams or streams | depends on the implementation | password or token | depends on the implementation; Hysteria2 can answer as an HTTP/3 site |
| **MASQUE CONNECT-IP** | L3, IP packets | HTTP/2 or HTTP/3 | one tunnel per session | like any IP packet | none | depends on the implementation | an HTTP server |
| **WireGuard, AmneziaWG** | L3 | its own UDP | none | like any IP packet | AWG has it | Curve25519 keys | silent to foreign packets |

What follows from the table:

- **With NaïveProxy** TrustTunnel shares the idea and the weak spot.
  NaïveProxy answers traffic analysis with padding and with Chromium's
  stack, that is, with a real browser's fingerprint. TrustTunnel has no
  padding, and its fingerprint depends on the client
  ([§3](#3-detectability-what-an-intermediary-sees)).
- **From an HTTPS proxy** it differs by HTTP/3, by frames for UDP and by a
  server that may decline to admit being a proxy when authorization is
  wrong.
- **From Trojan**, by multiplexing: dozens of browser connections travel in
  one TLS connection, not in dozens.
- **From Hysteria2 and TUIC**, by ordinary QUIC congestion control and by
  the ability to run over TCP when UDP is blocked.
- **From L3 tunnels**, by everything that follows from the difference
  between a proxy and a VPN ([§0.2](#02-a-proxy-not-a-vpn)): no ICMP other
  than echo, no arbitrary IP protocols, but also no MTU problems inside the
  tunnel.

---

# 3. Detectability: what an intermediary sees

There are three intermediaries, as in
[xray-protocols-explained §0](xray-protocols-explained.md#0-the-whole-picture-layers-and-threats):
a passive classifier that looks at the shape of traffic; active probing,
where the intermediary connects to the server itself; and recording
traffic for later. Against the third, TrustTunnel has nothing of its own
beyond the outer TLS or QUIC. Against the first two it has something, but
less than protocols designed for it.

> 🧭 **TL;DR:** TrustTunnel hides in the shape of HTTPS, but it does not
> hide TLS-in-TLS, and it does not hide the proxy from someone who sends it
> a `CONNECT`. The official client's fingerprint is Chrome; ours is Chrome
> only in part on HTTP/2 with uTLS, and quic-go on HTTP/3.

## 3.1 TLS-in-TLS without padding

Almost everything a user opens through the tunnel is HTTPS itself. Inside
the stream travels a second TLS handshake: the ClientHello to the target
site, the server's answer, the certificates. A classifier does not need to
decrypt them: the sizes and rhythm of the outer TLS records repeat the
shape of the nested handshake. This signal is covered in
[xray-protocols-explained §3.1](xray-protocols-explained.md#31-the-tls-in-tls-problem).

The protocol has no padding and no mechanism like Vision, so this signal
is visible on both transports and in every implementation, the official
one included. The specification itself admits that traffic patterns may
remain distinguishable.

## 3.2 Fingerprints

| What | Official client | Our outbound, HTTP/2 | Our outbound, HTTP/3 |
|---|---|---|---|
| ClientHello | per `tls_profile`, `chrome` by default; also `chrome_canary`, `safari`, `firefox`, `okhttp`, `openssl` | uTLS with `tls.utls` (`chrome` recommended), Go `crypto/tls` without it | quic-go, uTLS unavailable |
| HTTP/2 SETTINGS values and connection window | stream window 131072, Chrome's constant | Chrome's: `HEADER_TABLE_SIZE 65536`, `ENABLE_PUSH 0`, `INITIAL_WINDOW_SIZE 6291456`, `MAX_HEADER_LIST_SIZE 262144`, `WINDOW_UPDATE 15663105` | — |
| SETTINGS order, pseudo-headers | not described in the specification | Go's: order `2,4,5,6,1` instead of `1,2,4,6`, an extra `MAX_FRAME_SIZE 16384`, `:authority` before `:method`, HEADERS without priority | — |
| QUIC Initial and HTTP/3 SETTINGS | not described in the specification | — | quic-go |

Where each signal is visible:

- **The ClientHello** is visible to any observer on the path: in TLS it
  travels in clear text, in QUIC inside the Initial packet, whose keys are
  derived from the packet itself. It is the main fingerprint, and on HTTP/2
  uTLS covers it.
- **HTTP/2 SETTINGS and the pseudo-header order** travel inside TLS. Only
  whoever terminates TLS sees them: the server itself or a proxy in front
  of it. Our outbound repeats Chrome's values where `x/net/http2` allows it
  without a fork, and keeps Go's shape where it does not.
- **QUIC** in our HTTP/3 is quic-go throughout: the Initial, the
  ClientHello and the HTTP/3 SETTINGS.

> 🔀 Details and limits are in
> [SPEC 133 R13](../SPECS/TASKS/133-TRUSTTUNNEL_OUTBOUND/SPEC.md). The
> HTTP/2 fingerprint is pinned by the `TestHTTP2Preface` test, so an `x/net`
> update cannot change it unnoticed.

The official client also has an `anti_dpi` option: a 25 ms delay between
the first byte of the ClientHello and the rest. We have no separate key for
it; the closest built-in equivalent is `tls.fragment` of the shared TLS
layer, but that is a different split
([protocols-transports §6.5](protocols-transports.md#65-outbound-key-mapping)).

## 3.3 Active probing of the server

An active prober connects to a suspicious address itself and looks at the
answer.

**The official endpoint.** With an unknown SNI the prober gets a TLS drop
and nothing else. With the right SNI and no credentials it gets, by
default, 407 with `proxy-authenticate` to any request, a plain `GET`
included. That is an outright admission that a proxy lives there. The
operator can change it:

- `auth_failure_status_code` and `non_connect_auth_failure_status_code`
  switch the answer to 403, 404 or 405. The endpoint documentation warns
  that a code other than 407 breaks proxy authentication in Chrome, that
  is, using the endpoint as a browser proxy.
- `[reverse_proxy]` with its own hosts serves a real site from another
  server; the reachability-check and speed-test hosts answer with their own
  content.

**Our inbound.** Every request other than `CONNECT`, and `CONNECT _icmp`,
gets 404 with an empty body and no `proxy-authenticate`. HTTP/1.1 is not
served: on the stand `curl --http1.1` got a closed connection. 407 is sent
only to a `CONNECT` with wrong credentials. A scanner that sends `GET` sees
an empty web server.

> ⚠️ Neither hides the proxy from a prober that knows the protocol. A
> `CONNECT` without authorization gets 407 by default from both the
> endpoint and our inbound. The endpoint can be switched to 403, 404 or
> 405 for `CONNECT` as well; our inbound always answers 407 to `CONNECT`,
> and our outbound treats any of these codes as an authorization refusal.
> Deeper camouflage, such as a hand-off to a real site like `fallback` on
> `trojan` and `vless`, is a separate question
> ([SPEC 134 Q4](../SPECS/TASKS/134-TRUSTTUNNEL_INBOUND/SPEC.md)).

## 3.4 client_random rules

The endpoint has a filter at the TLS level, before any HTTP: the rules
file `rules.toml`. A rule can require a client address from a subnet
(`cidr`), a given start of the `client_random` field in the ClientHello
(`client_random_prefix`, a prefix or a prefix with a mask), or proof of
knowing a key (`client_random_auth_key`). Rules are evaluated in order and
the first match decides; if none matches, the connection is allowed.

The key variant works like this. The 32 bytes of `client_random` are split
in half: the first 16 are random in every handshake, the last 16 are
derived from the first, a shared key and the SNI. To an observer the
ClientHello stays random, while the server checks it deterministically and
silently drops strangers. This resembles how REALITY hides its secret in
the session id
([xray-protocols-explained §2.3](xray-protocols-explained.md#23-how-the-server-recognises-its-own-clients-session-id)).
If the file has even one key rule, connections without SNI are refused
outright.

We do not support `client_random` rules. The reason is the TLS stack:
`client_random` is generated by the TLS library itself, and the client
needs to put its own value there.

- On HTTP/3 the handshake is built by quic-go on the standard
  `crypto/tls`, so there it is impossible by construction.
- On HTTP/2 it is possible only by controlling `client_random` through the
  uTLS fork.
- The official client itself can do it only on AdGuard-patched OpenSSL;
  its MIPS builds lack the feature.

> ⚠️ **The refusal is indistinguishable from a wrong SNI.** A server with a
> `client_random` rule drops TLS without a word, and from the client side it
> looks exactly like an unknown name. The only way to tell is the export:
> if the config or the `tt://` link has `client_random_prefix` or
> `client_random_auth_key`, the node will not work here. Support is a
> separate task when a real request appears, and for HTTP/2 only.

---

# 4. The session model

There is one session per node, and it outlives individual connections. So
it has a lifecycle: when to open it, whether to keep it alive while idle,
and how to tell that it has died. The protocol does not fully answer these
questions, and implementations answer them differently.

> 🧭 **TL;DR:** the official client keeps its session alive; our node lets
> it die while idle and quietly rebuilds it on the next dial. While idle our
> node sends not a single packet, and groups see a node failure at dial
> time, not on a timer.

## 4.1 How the official client keeps its session

- **HTTP/3:** while idle the client sends ACK-eliciting packets every 30 s
  (its `timeout`, the connection timeout). The specification's QUIC idle
  timeout is `2 × (connection timeout + health check timeout)`, 74 s by
  default, so without these packets the session would expire while idle.
- **HTTP/2:** relies on `_check` health checks. This is **not a timer**.
  The check is triggered by tunnel events: a new connection opened, a
  connection migrating between upstreams, a flow-control update while the
  client waits for data from the server, an ICMP request, wake-up after
  sleep (forced). Checks are throttled to one per 30 s, and the answer
  deadline is 7 s. Any data from the server cancels a pending check, and a
  failure starts recovery with an increasing backoff.

> ⚠️ The numbers in `PROTOCOL.md` are easy to misread: 7 s is the answer
> deadline of a check, not an interval. The `sing-trusttunnel` library
> turned them into a timer: a check every 7 s while connections are kept
> idle.

## 4.2 How our node keeps its session

- **Lazy opening.** The constructor only validates the config; the session
  opens on the first request.
- **No keepalive.** No HTTP/2 PING, no QUIC keepalive. The client's QUIC
  idle timeout is 600 s, the same limit after which the server closes the
  session.
- **Idle teardown.** After `idle_timeout` (5 minutes by default, global
  default `lx.trusttunnel.idle_timeout`) without requests and without open
  streams, the node closes the session itself. On the stand HTTP/3 closed
  silently, and HTTP/2 with one TLS alert in each direction when the
  connection closed. `"0"` disables teardown.
- **Transparent rebuild.** A dial that finds the session dead (closed by
  the server, GOAWAY, QUIC idle timeout) or stale (silent for longer than
  the idle window, for example after the device slept) rebuilds it and
  repeats the `CONNECT` once within the same deadline. On a stale session
  the `CONNECT` gets 3 s, not the whole deadline: if NAT forgot the
  connection while idle, the dial does not hang until the router's
  timeout.
- **`health_check`.** Off by default. When on, it sends `_check` before a
  dial if the session has been silent for more than 30 s, and waits 7 s for
  the answer. A failure closes the session, and the dial that triggered the
  check rebuilds it. A check is never sent on a timer.
- **A network change** resets the session: QUIC migration is off on the
  endpoint, and without a reset the session would hang until the idle
  timeout.
- **One session per node.** At the server's stream limit a dial waits for
  a free stream within its deadline. The node never opens a second
  session, although Go's `http2.Transport` would do exactly that by
  default.

With this scheme `health_check` adds little: it finds a dead session one
round trip earlier than the `CONNECT` itself would. The key is kept for
compatibility with husi configs and works on events, as in the official
client.

## 4.3 Three approaches side by side

| | Official client | `sing-trusttunnel` | sing-box-lx |
|---|---|---|---|
| Packets while idle | HTTP/3: every 30 s; HTTP/2: only event-driven checks | `_check` every 7 s while connections are kept idle | none |
| `_check` | on events, at most once per 30 s, 7 s deadline | 7 s timer | optional: before a dial after 30 s of silence, 7 s deadline |
| Idle teardown | HTTP/3 keeps a keepalive | none | `idle_timeout`, 5 minutes |
| Lost or stale session | recovery with an increasing backoff | not handled | rebuild inside the dial, 3 s budget |
| Failed `CONNECT` | from the answer | on the first read | from the dial |

The last row for `sing-trusttunnel`: its `Dial` returns once the connection
to the server is obtained, before the `CONNECT` answer. A 407 or 502
surfaces on the first read, and a group does not see that the node failed
([SPEC 133 §7 Q1](../SPECS/TASKS/133-TRUSTTUNNEL_OUTBOUND/SPEC.md)).

## 4.4 What this means for the battery and for groups

**Battery.** A packet while idle wakes the radio. A packet every 30
seconds is two wake-ups a minute all night, even when the user is doing
nothing. The rule of the [ENERGY](../SPECS/FEATURES/008-ENERGY/FEATURE.md)
feature is that an idle node sends nothing, whatever the config. The price
is that the first dial after a long idle opens a new session: one TLS or
QUIC handshake. The Android AAR does not carry TrustTunnel yet, but a
laptop on battery pays for wake-ups the same way. The whole energy model is
in [lx-energy](lx-energy.md).

**Groups.** `urltest`, including its `mode: failover`, picks a node by the
results of dials. If a node failure is visible only on the first read, the
group counts the dial as a success and keeps a dead node. If a stale
session produces a spurious failure, the group leaves a live node. Our node
closes both cases: a server refusal arrives as a dial error with the code
and `X-Warning`, and a dead session is rebuilt inside the dial and never
surfaces. On the stand, `urltest` through a node whose session the server
had closed reported the node as available.

---

# 5. The implementation in sing-box-lx

## 5.1 Status and builds

| | |
|---|---|
| State | Ships since lx.21 |
| Build tag | `with_trusttunnel`, in `LX_TAGS` since 2026-10-16; HTTP/3 also needs `with_quic`, which is in the set |
| Where it is | Desktop binaries and static router builds |
| Where it is not | The Android AAR (LxBox) |
| Without the tag | `trusttunnel is not included in this build, rebuild with -tags with_trusttunnel` when the config loads; the rest of the core is upstream byte for byte |
| In the UI | The Clash API and libbox label the type "TrustTunnel" |
| Discussion | [Leadaxe/sing-box-lx#46](https://github.com/Leadaxe/sing-box-lx/issues/46) |

The request came from NekoBox/NyameBox, which is building its next version
on sing-box-lx and already carried the protocol in its fork
([PR #45](https://github.com/Leadaxe/sing-box-lx/pull/45)). Upstream has
the protocol in none of its branches (checked 2026-10-15).

## 5.2 Two layers

The implementation is our own, without a third-party library, and is split
into two layers:

| Layer | Where | What is inside |
|---|---|---|
| Protocol core | `transport/trusttunnel/` | Client and server: session, `CONNECT` streams, the `_udp2` codec, `_check`, Basic, errors. Imports only `sagernet/sing`, `x/net/http2`, and in the `with_quic` files `sagernet/quic-go` and `sagernet/sing-quic`. No sing-box `adapter`, `option` or `log` |
| sing-box adapters | `protocol/trusttunnel/outbound.go`, `inbound.go` | Dial fields, DNS, lifecycle, logging, mapping of options onto the core API |
| Options | `option/trusttunnel.go` | `TrustTunnelOutboundOptions`, `TrustTunnelInboundOptions` |
| Registration | `include/trusttunnel.go`, `include/trusttunnel_stub.go` | The types behind the tag and a stub without it |

The core boundary is the main architectural decision. A package with no
dependency on sing-box internals can be lifted into its own `sing-*` style
module, sent upstream, or taken by husi and NekoBox as is. A review that
finds a sing-box type in the core is a review that fails.

Edits to upstream files are 10 lines in marked `// lx:` blocks: 6 in
`include/registry.go` (outbound and inbound registration) and 4 in
`constant/proxy.go` (the type constant and the UI label). The new files are
named and laid out the way upstream lays out a tagged protocol (`naive`,
`tailscale`, `cloudflared`), without `_lx` suffixes.

> 🔀 Why not `sing-trusttunnel` v0.3.2: its dial returns before the
> server's answer, its check is a 7 s timer, and it has no idle teardown
> and no stale-session handling. mihomo carries a copy of v0.1.1 of that
> library. The wire format was checked against `PROTOCOL.md`, the endpoint
> code and the official client, not against them.

## 5.3 Outbound

The `trusttunnel` outbound is a node on a par with the others: TCP and UDP,
usable in groups, under `detour` and as a `chain` link. The essentials of
its behaviour:

- **The dial waits for the server's answer.** 200 is a connection;
  anything else is a dial error with the code and `X-Warning`, for example
  `trusttunnel: server responded 502 Bad Gateway: 300 - Connection failed for some reason`.
  An authorization refusal (407, and also 403, 404, 405) additionally tears
  the session down.
- **TLS and SNI are mandatory.** A `server` given as an IP address without
  `tls.server_name` is a start error, because the server routes by SNI
  only and Go sends no SNI for an IP address. Without this rule the symptom
  would be a silent TLS drop.
- **DNS asymmetry.** Domains of TCP destinations go to the server
  unresolved and are resolved there. A UDP frame carries an IP only, so the
  domain of a UDP destination is resolved locally: the node's
  `domain_resolver`, else `route.default_domain_resolver`, else the DNS
  rules ([§6.4](#64-dns-for-udp-a-resolver-through-the-node)).
- **The session** follows the rules of
  [§4.2](#42-how-our-node-keeps-its-session).

## 5.4 Inbound

The `trusttunnel` inbound turns sing-box into a server for the official
clients and for our outbound. Each TCP `CONNECT` and each UDP flow of a
`_udp2` stream goes to the router as from any other inbound: with
`inbound`, `user`, the client address and the destination (a domain stays
a domain). What sets it apart from the official endpoint:

- **`network` selects listeners, not tunnelled networks.** `tcp` is HTTP/2
  over TLS, `udp` is HTTP/3 over QUIC, both by default, as on the upstream
  `naive` inbound. TCP and UDP are always tunnelled; restrict them with
  route rules. On the outbound `network` means the opposite: which networks
  the node carries.
- **ALPN is set by the inbound:** `h2` first on TCP, `h3` last on UDP; an
  explicit list is kept and completed.
- **The 200 follows the dial result.** The answer goes out when the router
  first reads from the connection, that is, after the connection to the
  target succeeded. If an error comes first, the client gets 502 with
  `X-Warning` (300, or 302 for a timeout), exactly as from the official
  endpoint. With a `sniff` route action the router reads before dialing,
  the 200 goes out early, and a later failure looks like a stream close,
  as on `naive`.
- **Limits match the endpoint's defaults**, without keys: 1000 streams on
  HTTP/2, 4096 on HTTP/3, a 600 s session idle timeout, 0-RTT off.
- **UDP source.** The official client puts the real source into the frame,
  our outbound and mihomo a zero one. A zero source is replaced by the
  session address, so `source_ip` route rules keep meaning the client. The
  `app` field is read, length-checked and ignored.
- **Authorization refusal** is 407 with
  `proxy-authenticate: Basic realm=Authorization Required`; the log line
  names the source address, never the credentials.

> 🔀 The inbound is a tunnel endpoint, not the AdGuard product. There are
> no `client_random` rules, no subscription, no reachability-check or
> speed-test hosts, no reverse proxy, no ICMP and no SOCKS5 upstream.
> Routing, access and address filtering are sing-box rules.

## 5.5 What was verified on the stand

The stand [`lx-test/trusttunnel/`](../lx-test/trusttunnel/README.md) runs
on loopback with the **official** binaries: endpoint 1.1.0 and CLI client
1.1.11, with release signatures checked. Taken on it (2026-10-15 and
2026-10-16):

| Direction | HTTP/2 | HTTP/3 |
|---|---|---|
| Our outbound → official endpoint | yes | yes |
| Official client → our inbound | yes | yes |
| Our outbound → our inbound | yes | yes |

In addition:

- UDP through `_udp2`, including DNS queries and a 9100-byte datagram
  echoed there and back, in every direction. The ceiling here is the
  sender (`net.inet.udp.maxdgram` on macOS), not the tunnel.
- A wrong password is a `407 Proxy Authentication Required` dial error, on
  the endpoint and on our inbound; the official client gets 407 with
  `proxy-authenticate` and exits.
- A `CONNECT` to a closed port is `502 Bad Gateway: 300 - …` from the dial,
  from both the endpoint and our inbound.
- Stale session: after the server closed it, the first dial rebuilds the
  session without an error; with a "frozen" NAT the `CONNECT` hits the 3 s
  budget, then a rebuild and 200.
- Two minutes idle with a live session: not a single byte in either
  direction, with and without `health_check`, on both transports.
- `detour` through a neighbouring sing-box and `chain` in both orders.
- The official client in `auto` mode against an inbound with `tcp` only
  picks HTTP/2 and works.
- A route rule `ip_is_private` + `reject` on the inbound blocks a private
  target, while the internet through the same inbound keeps working.

Not verified: a live server on the internet (own VPS or a public one),
ICMP on the client (CLI 1.1.11 has no setting for it), the 600 s idle
timeout of the inbound over HTTP/3.

## 5.6 What is not supported

| What | Why |
|---|---|
| ICMP (`_icmp`) | In the sing-box core this is a separate ICMP routing path; deferred until requested. The inbound answers `_icmp` with 404, and an official client with ICMP enabled keeps working over TCP and UDP |
| `client_random` rules | Needs control over `client_random` in the TLS stack ([§3.4](#34-client_random-rules)) |
| `upstream_protocol: auto` | One transport per node (`quic`); "auto" is a `urltest` group of two nodes ([§6.1](#61-http2-or-http3)) |
| `tt://` links and subscriptions | The core receives ready JSON; import is the job of the launcher and LxBox. The inbound does not serve a subscription |
| Reverse proxy, reachability-check and speed-test hosts, SOCKS5 upstream | These are the product around the tunnel, not the protocol |
| Several sessions per node, a pool | One session by design ([§4.2](#42-how-our-node-keeps-its-session)) |
| `anti_dpi` as a separate key | The closest equivalent is `tls.fragment` |
| HTTP/2 without TLS (h2c) behind a terminating proxy | `tls.enabled: false` is an error; to be opened on request |

## 5.7 Examples

A minimal outbound. The `tls.utls` block with `chrome` is the recommended
shape for HTTP/2:

```json
{
  "type": "trusttunnel",
  "tag": "tt-node",
  "server": "vpn.example.com",
  "server_port": 443,
  "username": "user",
  "password": "secret",
  "tls": {
    "enabled": true,
    "server_name": "vpn.example.com",
    "utls": { "enabled": true, "fingerprint": "chrome" }
  }
}
```

The same node over HTTP/3: `"quic": true`, and remove the `utls` block,
otherwise the start fails with `uTLS is not supported with quic`.

A minimal inbound:

```json
{
  "type": "trusttunnel",
  "tag": "tt-in",
  "listen": "::",
  "listen_port": 443,
  "users": [
    { "username": "user", "password": "secret" }
  ],
  "tls": {
    "enabled": true,
    "server_name": "vpn.example.com",
    "certificate_path": "/etc/sing-box/fullchain.pem",
    "key_path": "/etc/sing-box/key.pem"
  }
}
```

Full examples with every key are in
[protocols-transports §6.9](protocols-transports.md#69-examples).
Converting the official client's config (TOML, the endpoint's export, a
`tt://` link) and sing-box-extended configs is covered by the tables in
[§6.5 for the outbound](protocols-transports.md#65-outbound-key-mapping)
and [§6.8 for the inbound](protocols-transports.md#68-inbound-key-mapping).
The key schema matches husi, so its configs are read without renaming; the
differences are the added `idle_timeout` and an empty `password`, which is
an error here.

---

# 6. Practical guidance

## 6.1 HTTP/2 or HTTP/3

| | HTTP/2 (`quic: false`) | HTTP/3 (`quic: true`) |
|---|---|---|
| ClientHello fingerprint | uTLS, Chrome | quic-go, uTLS unavailable |
| A network where UDP is blocked or throttled | works | does not work |
| Packet loss | one loss stalls every connection of the node: they share one TCP connection | a loss delays only its own stream |
| Network change | a new session | a new session (migration is off on the endpoint) |
| Congestion control | the OS's TCP | `quic_congestion_control`: `bbr` by default, `cubic`, `reno` |

The practical rule: HTTP/2 with `utls` `chrome` by default, HTTP/3 where
the network loses packets but lets UDP through. If you do not know which is
better on a given network, put two nodes for the same server into a
`urltest` group. That replaces the official client's `auto` mode: the
client picks a transport at connection time, the group picks by
measurements.

```json
{
  "type": "urltest",
  "tag": "tt-auto",
  "outbounds": ["tt-h2", "tt-h3"]
}
```

## 6.2 SNI and the certificate

- **The SNI must match the server's host.** On the official endpoint that
  is the `hostname` of `main_hosts`. From an export, `tls.server_name`
  takes `custom_sni` if it is set, else `hostname`.
- **Is `server` an IP address?** Then `tls.server_name` is mandatory,
  otherwise the node does not start.
- **A self-signed certificate.** The endpoint wizard can issue one and
  embed it into the export. In sing-box it goes into `tls.certificate` as
  PEM. A `tt://` link carries the chain as DER, the TOML export as PEM; the
  importer converts DER.
- **`skip_verification`** of the official client is `tls.insecure`. The
  specification requires certificate verification, so this is a last
  resort.
- **For our inbound** the certificate must cover the name clients will
  send in SNI. The official clients verify the certificate: with a
  self-signed one, give it to them in their config (`certificate`).

## 6.3 Private networks on the inbound

By default the official endpoint does not let clients into its private
network (`allow_private_network_connections = false`). Our inbound has no
such setting: it is a route rule, set explicitly.

```json
{
  "route": {
    "rules": [
      { "inbound": "tt-in", "ip_is_private": true, "action": "reject" }
    ]
  }
}
```

> ⚠️ Without this rule any user of the inbound reaches the server's local
> network and its loopback services. This is deliberate sing-box behaviour:
> access is decided by rules, not by protocol settings.

The other endpoint settings map the same way: refusing IPv6 is route rules
or the DNS `strategy`, the UDP flow inactivity is `udp_timeout` of a
`route-options` rule. The full table is
[protocols-transports §6.8](protocols-transports.md#68-inbound-key-mapping).

## 6.4 DNS for UDP: a resolver through the node

TCP and UDP of the same node resolve domains in different places
([§5.3](#53-outbound)): TCP with the server's DNS, UDP locally. If the
local resolver goes around the tunnel, UDP destinations are resolved
outside the tunnel and may get a different answer than TCP, for example
from geo-DNS.

The recommended shape is a node `domain_resolver` that itself goes through
the node:

```json
{
  "dns": {
    "servers": [
      { "type": "udp", "tag": "dns-tt", "server": "1.1.1.1", "detour": "tt-node" }
    ]
  },
  "outbounds": [
    {
      "type": "trusttunnel",
      "tag": "tt-node",
      "server": "203.0.113.10",
      "server_port": 443,
      "username": "user",
      "password": "secret",
      "domain_resolver": { "server": "dns-tt", "strategy": "ipv4_only" },
      "tls": { "enabled": true, "server_name": "vpn.example.com" }
    }
  ]
}
```

- The DNS server is given as an IP address, so a query to it through the
  node needs nothing resolved.
- `strategy: "ipv4_only"` is the mapping of `has_ipv6 = false` from the
  export: such a server refuses IPv6 destinations. On a server with IPv6 it
  can be dropped.

> ⚠️ The same `domain_resolver` also resolves the server's own address. If
> `server` is a domain, a resolver that goes through this very node cannot
> resolve it: connecting to the node needs the address, and the address
> needs the connection. In that case give `server` as an IP address with
> `tls.server_name`, as above, or resolve UDP destinations earlier, with a
> `resolve` route action on a DNS server that goes through the node (this
> is how the stand is set up).

## 6.5 Typical failures

| Symptom | Cause | What to do |
|---|---|---|
| TLS drops at once, no HTTP answer, nothing useful in the log | The SNI did not match the server's host; or the server has `client_random` rules | Compare `tls.server_name` with `hostname`/`custom_sni` of the export; if the export has `client_random_*`, the node is not supported ([§3.4](#34-client_random-rules)) |
| `tls.server_name is required when server is an IP address…` at start | `server` is an IP address | Add `tls.server_name` |
| `trusttunnel: server responded 407 Proxy Authentication Required` | Wrong `username`/`password` | Compare with the export; the session is already closed, the next dial opens a new one |
| `502 Bad Gateway: 300 - …` to an address in a local network | The endpoint refuses private networks; or an `ip_is_private` rule on our inbound | Expected; allow it on the server if needed |
| `502 … 310` or `311` with a hostname | The server's DNS returned a non-routable address or loopback | A DNS problem on the server side |
| IPv6 destinations fail | `ipv6_available = false` on the endpoint | `domain_resolver.strategy: "ipv4_only"` on the node |
| UDP to `::a.b.c.d` or `::1` goes to the wrong place | The frame format ([§1.4](#14-udp-the-_udp2-stream-and-its-frames)) | Use the IPv4 form or another address |
| `uTLS is not supported with quic` | `quic: true` together with `tls.utls` | Remove `utls` or pick HTTP/2 |
| `tls.alpn must contain h2` | An explicit `tls.alpn` without the needed value | Remove `alpn` or add `h2` (`h3` with `quic`) |
| `trusttunnel: HTTP/3 requires the with_quic build tag` | A custom build without `with_quic` | Build with the tag or switch to HTTP/2 |
| `trusttunnel is not included in this build…` | A build without `with_trusttunnel`, for example the Android AAR | Use a release desktop or router binary |
| `outbounds[N].network: unknown network: icmp` | `icmp` in `network` | Remove it: ICMP is not tunnelled |

---

# 7. Glossary

| Term | Meaning |
|---|---|
| **Endpoint** | A TrustTunnel server; the official one is `trusttunnel_endpoint` in Rust |
| **Session** | One HTTP/2 or HTTP/3 connection between client and server |
| **Stream** | A single HTTP request inside a session: a TCP connection, a `_udp2` flow, a `_check` |
| **`CONNECT`** | The HTTP method "connect me to `host:port`"; after a 200 answer the stream becomes a byte channel |
| **Pseudo-host** | A reserved `:authority` value without a port: `_udp2`, `_check`, `_icmp` |
| **`X-Warning`** | The header of a 502 refusal, with a code 300–311 and an explanation |
| **`main_hosts`** | The tunnel hosts on the endpoint; selected by SNI |
| **`client_random`** | The 32 random bytes of the ClientHello; the endpoint can filter clients by them |
| **TLS-in-TLS** | A nested handshake inside the tunnel, visible by record sizes |
| **Stale session** | A session silent for longer than the idle window; the next `CONNECT` on it checks whether it is alive |
| **Idle window** | The node's `idle_timeout`; after it the session is closed locally |
| **Protocol core** | The `transport/trusttunnel/` package, with no dependency on sing-box |

---

## See also

- **[protocols-transports.md §6](protocols-transports.md#6-trusttunnel-outbound-and-inbound)**:
  every field, default, error verbatim; §6.4 session behaviour, §6.5 and
  §6.8 key-mapping tables, §6.9 examples.
- **[lx-energy.md](lx-energy.md)**: the energy model and `mode: failover`.
- **[xray-protocols-explained.md](xray-protocols-explained.md)** and
  **[amneziawg-explained.md](amneziawg-explained.md)**: the same format for
  the Xray protocols and for AmneziaWG.
- Feature spec: [018-TRUSTTUNNEL](../SPECS/FEATURES/018-TRUSTTUNNEL/FEATURE.md);
  tasks: [133](../SPECS/TASKS/133-TRUSTTUNNEL_OUTBOUND/SPEC.md) (outbound
  and the client half of the core),
  [134](../SPECS/TASKS/134-TRUSTTUNNEL_INBOUND/SPEC.md) (inbound and the
  server half).
- Stand: [lx-test/trusttunnel/](../lx-test/trusttunnel/README.md).
- Official repositories:
  [TrustTunnel/TrustTunnel](https://github.com/TrustTunnel/TrustTunnel)
  (endpoint, `PROTOCOL.md`, `CONFIGURATION.md`),
  [TrustTunnel/TrustTunnelClient](https://github.com/TrustTunnel/TrustTunnelClient)
  (libraries and CLI client),
  [TrustTunnel/TrustTunnelFlutterClient](https://github.com/TrustTunnel/TrustTunnelFlutterClient)
  (app).
- Code: `transport/trusttunnel/` (core), `protocol/trusttunnel/`
  (adapters), `option/trusttunnel.go` (options).
