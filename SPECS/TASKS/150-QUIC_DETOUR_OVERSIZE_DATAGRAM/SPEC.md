# SPEC: 150 — QUIC_DETOUR_OVERSIZE_DATAGRAM

> 🌐 Русская версия: **[SPEC.ru.md](SPEC.ru.md)** (auxiliary copy; this English file is primary — owner decision 2026-11-02).

**Feature:** [009-MASQUE_WARP](../../FEATURES/009-MASQUE_WARP/FEATURE.md) (origin and the main path); the hysteria2/tuic part is a held patch over upstream files and carries a row in the [004-HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md) registry.

| Field | Value |
|-------|-------|
| Type | B (bug) — a QUIC client (`masque` h3, upstream `hysteria2`, `tuic`) dies a few seconds after the handshake when its UDP runs through a stream-tunnelled `detour` |
| Status | I (implemented) — 2026-11-02: helper, three seams, red/green units in the tree; the final build on the owner's Mac kept h3 alive through the n009 relay in plain and XUDP packing (4 of 4 pings over 20 s, where it died at 1–5 s before); the second relay of the 2026-11-01 stand (n002) had broken on its own that day (vless TCP ping failed, QUIC through it landed on a foreign key); the reporter's device and live hysteria2/tuic servers not run |
| Branch | `lx` |
| Build tag | — (`common/lxdatagram`, `protocol/masque` build always; the `protocol/hysteria2` and `protocol/tuic` seams sit under upstream's `with_quic`) |
| Origin | Telegram report by zomg 2026-11-01 (LxBox 2.25.19-rc.1, core `v1.14.4-lx.28`): the node "WARP (MASQUE) h3" with `detour` through free `[BL]` relays never comes up, while MASQUE h2 and AWG through the same relays work. Stand on the owner's Mac 2026-11-01 against the relays of `igareck/vpn-configs-for-russia`, decisive measurement with a debug build the same evening |
| Related | [146](../146-MASQUE_CLOSE_WEDGE_DETOUR_DEADLINE/SPEC.md) (the other half of the detour conn contract: deadlines; its wrapper sits above this one, order pinned by a test), [021](../021-MASQUE_CONNECT_IP_OUTBOUND/SPEC.md) (the outbound), [074](../074-MASQUE_VHTTP_AUTO/SPEC.md) (`auto` masked the failure by falling back to h2), [120](../120-PATH_MTU_ALIGN/SPEC.md) (PMTUD on the inner QUIC — the other direction of the same mechanism), [028](../028-NESTED_TUNNEL_UDP_FRAGMENT/SPEC.md) (nested tunnels and datagram size), [091](../091-CONFIG_VALIDATION_TUIC_MASQUE/SPEC.md) (the only other `// lx:` seam in `protocol/tuic/outbound.go`) |

A kernel UDP socket silently truncates a datagram that does not fit the caller's buffer. A UDP
"socket" tunnelled through a stream `detour` (vless XUDP or plain vless UDP over WebSocket, gRPC,
XHTTP, TCP, …) does not: sing's packet readers answer with `io.ErrShortBuffer`, quic-go treats
that read error as fatal and closes the transport. Cloudflare WARP sends twelve Path-MTU probes of
1453–1462 bytes a few seconds into every h3 session, 1–10 bytes above the 1452 that quic-go
advertises, so every MASQUE h3 tunnel through such a detour dies at that moment. The fix gives the
detour conn kernel semantics: a read into a short buffer returns the truncated head of the datagram
and drops the rest, exactly what the OS does on a real socket. Upstream `hysteria2` and `tuic`
hand quic-go their detour conn through the same read path and get the same wrapper.

## 1. Problem

### 1.1 The stand

Reporter's picture: h3 to WARP via `detour` → selector of `[BL]` relays — "dial quic: context
deadline exceeded" / `handshake_failure` / no connection; h2 and AWG via the same detour fine.

On the Mac, with the WARP keys of the owner's launcher config and seven relays from the list,
three independent layers were separated:

1. **Xray QUIC sniffing on part of the VPS relays.** With `sniffing.destOverride` and without
   `routeOnly` the relay sends UDP/QUIC to the address that the SNI resolves to, not to the
   requested IP. Proof: SNI `www.apple.com` → remote alert `0x150 200:internal error` (Akamai);
   `www.cloudflare.com` → "different public key" (an ordinary edge, not the MASQUE endpoint);
   a name without an A record → timeout. This is where the reporter's `handshake_failure` with
   `telemost.yandex.ru` from the LxBox SNI pool comes from. TLS over TCP (h2) is not rewritten
   there. Not ours to fix; the advice is a neutral SNI or h2.
2. **Relays without UDP at all** (onrender fronts). Not ours either.
3. **Through every relay that does pass UDP** (VPS with REALITY in Poland, ws behind
   Cloudflare:2083), plain vless UDP and XUDP alike: the h3 tunnel comes up, the first ping may even
   pass, and 1–5 s later `masque: tunnel died: read from tunnel: use of closed network connection`,
   on one relay with the cause visible: `quic: transport closed: short buffer`. The pre-SPEC-146
   binary behaves identically, so it is not a regression of lx.28.

Layer 3 is the subject of this task. A debug build with a wrapper that read every detour datagram
into a 64 KiB buffer, logged it and truncated it to the caller's length showed, through both relays
and in both packings, **exactly twelve oversize datagrams after the handshake and none after**:

| Wire length | Count | Over 1452 by | First byte |
|---|---|---|---|
| 1462 | 3 | 10 | `0x4x`/`0x5x` (1-RTT short header) |
| 1457 | 3 | 5 | same |
| 1454 | 3 | 2 | same |
| 1453 | 3 | 1 | same |

A descending ladder, three attempts per size, then silence: this is the server's DPLPMTUD
(RFC 8899) looking for a path ceiling above what the client advertised. **With truncation the h3
tunnel stayed alive through both relays**: 4 pings in 20 s, 355–536 ms after the first. One of the
three relays dropped its TCP side on its own (EOF) — that is its availability, not this bug.

### 1.2 Why a kernel socket survives and a stream detour does not

quic-go (pin `v0.61.0-sing-box-mod.12`) advertises `max_udp_payload_size = protocol.MaxPacketBufferSize = 1452`
(`connection.go:338/470`) and reads every datagram into a 1452-byte buffer:

- `basicConn.ReadPacket` (a `net.PacketConn` handed in, our `masque` path through
  `bufio.NewUnbindPacketConn`) → `ReadFrom(buffer.Data[:1452])`; the comment there says it: a
  bigger packet "will then end up undecryptable" — i.e. is expected to be truncated and dropped;
- `basicConnectedConn.ReadFrom` (a `net.Conn` handed in via `quic.DialConn`/`DialEarlyConn`, the
  `sing-quic` path of `hysteria2` and `tuic`) → `conn.Read(p)` with `len(p) == 1452`.

`Transport.listen` keeps going on `net.Error.Temporary()` and on the platform "message too long"
errno (`EMSGSIZE`, `WSAEMSGSIZE` — Windows reports a truncated datagram as an error, quic-go
explicitly ignores it); **any other read error closes the transport**. `io.ErrShortBuffer` is
neither.

On a real socket the kernel copies the first `len(p)` bytes and discards the tail (Linux, darwin,
Android); the truncated packet fails to decrypt and is dropped, the server counts the probe lost and
stays at 1452. That is the contract quic-go is written against: an oversize datagram is dropped,
never fatal.

Through a stream detour the datagram arrives whole, with its own length prefix, and the sing
readers refuse to hand out a part of it:

- `vless.PacketConn.Read` (`sing-vmess/vless/client.go:215`): `if len(b) < int(length) { return 0, io.ErrShortBuffer }`;
- `vless.XUDPConn.ReadFrom` → `buf.With(p)` → `ReadPacket` → `buffer.ReadFullFrom(c.Conn, length)` →
  `io.ErrShortBuffer` from `common/buf`.

The error is not temporary, quic-go closes the transport, the next read returns "use of closed
network connection", `masque` tears the tunnel down. With `vhttp: auto` (SPEC 074) the node then
quietly lands on h2 and the user sees nothing but "h3 does not work through this relay"; with
`vhttp: h3` the node is dead.

The SPEC 146 deadline wrapper is not involved: `deadline.Reader.pipeRead(len(p))` allocates a
buffer of the caller's length and passes the error through unchanged. The old binary without it
dies the same way.

Whether a server may exceed the peer's `max_udp_payload_size` is Cloudflare's business (RFC 9000
§18.2 only says such datagrams "are not likely to be processed by the receiver"); a kernel socket
makes the question moot, and so must we. Raising the quic-go constant would only move the
threshold: probes always go above the advertised value, that is their purpose.

### 1.3 Who is affected

- **`masque` h3 through any detour whose UDP is carried in a stream**: vless/vmess/trojan over
  WebSocket, gRPC, XHTTP, plain TCP, in plain and XUDP packing. This is the reporter's case and
  any "WARP behind a relay" setup; `vhttp: auto` hides it as a permanent h2.
- **Upstream `hysteria2` and `tuic` through the same detours**: the same 1452-byte read, the same
  readers, the same fatal path. Any server-side datagram above 1452 (a PMTUD probe, a server that
  sends 1452 of payload plus an obfuscation header) kills the session. Reasoned from the code,
  not measured on a live server: no hysteria2/tuic server behind a relay was at hand. The
  `hysteria2` port-hopping conn (`hysteria.HopConn`) already reads into a large buffer and copies,
  i.e. truncates — the one path inside `sing-quic` that already behaves like a socket.
- **Not affected**: the direct path (`dialer.udpConn` over `*net.UDPConn`, kernel truncation);
  detours that end in a datagram socket (an endpoint's netstack, a UDP-based proxy) deliver
  datagrams as datagrams — not measured on this stand, outside this task.

## 2. Fix

New logic in a new package, one seam per protocol. No sing or quic-go code is copied or changed
(pins untouched; nothing goes upstream).

### 2.1 `common/lxdatagram` — kernel semantics for a stream-carried datagram conn

`TruncateReads(conn net.Conn, logger logger.Logger) net.Conn`:

- **Kernel socket passes through untouched.** If `N.UnwrapReader(conn)` implements `syscall.Conn`
  the conn is a real socket (the direct path's `dialer.udpConn` says so itself and unwraps to
  `*net.UDPConn`); it is returned as is, no copy, no extra layer. The check is the same one
  `sing-quic` uses to decide whether it holds a socket (`withSyscallConn`). A stream detour
  stops unwrapping at the framing layer (`XUDPConn`, `vless.PacketConn`: readers that are not
  replaceable) and is not a `syscall.Conn`.
- **Otherwise** `Read(p)` reads the next datagram into the wrapper's own 65535-byte buffer (one per
  conn, serialised by a mutex) and copies `min(n, len(p))` bytes into `p`. A datagram longer than
  `p` returns `len(p), nil`; the tail is discarded and never carried into the next `Read` —
  datagram boundaries are kept, as on a socket. A `p` of 65535 bytes or more is read into directly.
- The first truncation on a conn is logged once at debug with both lengths; this is routine PMTUD
  behaviour, not a warning.
- The wrapper is transparent to the stacks around it: `Upstream()` returns the wrapped conn, so
  `deadline.NeedAdditionalReadDeadline` (SPEC 146) still sees `XUDPConn`'s answer through it;
  `ReaderReplaceable()` is false (the wrapper must stay in the read path); everything else
  (`Write`, `Close`, deadlines, addresses) is the embedded conn's.

`Dialer(dialer N.Dialer, logger logger.Logger) N.Dialer` wraps the result of `DialContext` for
`udp` and returns everything else (`tcp`, `ListenPacket`) unchanged. It is the seam for code that
receives a dialer and dials inside a library (`sing-quic`).

### 2.2 `masque`

`connectH3WithBudget` (`protocol/masque/outbound.go`, `// lx: SPEC 150`): the conn from
`o.dialer.DialContext` goes through `TruncateReads` first and the SPEC 146 deadline check runs on
the result, so the chain towards quic-go is `UnbindPacketConn → deadline.Conn → truncating conn →
detour conn`. The truncating layer reads with its own full-size buffer, so it keeps the datagram
whole on either side of the deadline wrapper (`deadline.Reader` reads into a buffer of the
caller's length and would pass a short-buffer error up unchanged); it sits closest to the detour
conn so the deadline wrapper keeps reading a plain conn. Both orders are pinned by a test (§3),
together with the bare deadline wrapper reproducing the field failure.

### 2.3 `hysteria2`, `tuic`

One line each (`// lx: SPEC 150`): the `Dialer` handed to `hysteria2.NewClient` /
`tuic.NewClient` is `lxdatagram.Dialer(outboundDialer, logger)`. Everything the two clients do
with the dialer stays as it is: `sing-quic` dials inside `dialFunc` / `offerNew`, stacks
Salamander/Gecko/HopConn on top (hysteria2) and hands the result to `qtls.Dial`/`CreateTransport`;
the wrapper sits below the obfuscation, where the raw datagram comes in. The two clients do not
type-assert their dialer (checked in the pin), and `hysteria2`'s own use of `outboundDialer` for
realm resolution (`ResolveDialer.QueryOptions`) keeps the unwrapped dialer. On the direct path
the wrapper is a no-op (§2.1), so the hot path of both protocols does not change.

### 2.4 Out of scope

- The SPEC 146 class (Close waiting for a dial through a detour conn without deadlines) in
  `hysteria2`/`tuic`: the helper deliberately does not add the deadline wrapper; that is a
  separate decision with a per-datagram goroutine cost.
- `vless.PacketConn`/`XUDPConn` semantics in sing: not changed, not copied (wrapper at the
  injection point, owner rule 2026-09-14).
- Inbounds / server side: untouched.
- The relays' Xray sniffing and UDP-less fronts (§1.1 layers 1–2): advice to the reporter — h2 or
  `vhttp: auto`, a neutral SNI, `162.159.198.1`/`.2` as the server.

## 3. Verification

- `common/lxdatagram/truncate_test.go`:
  - an oversize datagram → `n == len(p)`, `err == nil`, the next `Read` returns the **next**
    datagram, not the tail; exact-size and shorter datagrams untouched;
  - `*net.UDPConn` (`net.ListenUDP`) and `dialer.udpConn` pass through unwrapped;
  - `Dialer` wraps `udp` and leaves `tcp`/`ListenPacket` alone;
  - composition with the SPEC 146 wrapper truncates in either order (`deadline.NewConn` over
    `TruncateReads` and the reverse); the bare deadline wrapper alone reproduces the field
    `io.ErrShortBuffer`;
  - `NeedAdditionalReadDeadline` is seen through the wrapper.
- `protocol/masque/oversize_datagram_lx_test.go` — red/green on the production path
  `connectH3WithBudget`: a fake detour conn that frames datagrams like `vless.PacketConn`
  (length prefix, `io.ErrShortBuffer` when the caller's buffer is shorter than the frame) and
  delivers a 1462-byte frame as the first "server" datagram. Before the fix the dial ends at once
  with `short buffer` in the error chain; after it the frame is dropped and the dial ends by the
  context deadline (no server answers) — `errors.Is(err, context.DeadlineExceeded)`.
- `go test -race` with the lx-ci tags for `common/lxdatagram`, `protocol/masque/...`,
  `protocol/hysteria2`, `protocol/tuic`; `go vet`, gofmt; `go build` with `LX_TAGS`.
  `common/lxdatagram`, `protocol/hysteria2`, `protocol/tuic` added to the lx-ci test list (a
  package without a line there runs nowhere).
- Live, Mac, home uplink: the final build against the two relays of §1.1 that pass UDP, h3 ping
  through the detour must survive past the 5-second mark where it died before.
- Device (LxBox): the reporter's "WARP (MASQUE) h3" node with `detour` through a `[BL]` relay that
  passes UDP. Behind the reporter.

## 4. Files

- `common/lxdatagram/truncate.go` — `TruncateReads`, `Dialer` (new)
- `common/lxdatagram/truncate_test.go` (new)
- `protocol/masque/outbound.go` — `connectH3WithBudget` (`// lx: SPEC 150`, one line before the SPEC 146 block)
- `protocol/masque/oversize_datagram_lx_test.go` (new)
- `protocol/hysteria2/outbound.go` — `Dialer:` in `hysteria2.NewClient` (`// lx: SPEC 150`)
- `protocol/tuic/outbound.go` — `Dialer:` in `tuic.NewClient` (`// lx: SPEC 150`)
- `.github/workflows/lx-ci.yml` — test list
