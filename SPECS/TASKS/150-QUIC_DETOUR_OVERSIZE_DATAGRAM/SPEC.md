# SPEC: 150 — QUIC_DETOUR_OVERSIZE_DATAGRAM

> 🌐 Русская версия: **[SPEC.ru.md](SPEC.ru.md)** (auxiliary copy; this English file is primary — owner decision 2026-11-02).

**Feature:** [009-MASQUE_WARP](../../FEATURES/009-MASQUE_WARP/FEATURE.md) (origin and the main path); the hysteria2/tuic part is a held patch over upstream files and carries a row in the [004-HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md) registry.

| Field | Value |
|-------|-------|
| Type | B (bug) — a QUIC client (`masque` h3, upstream `hysteria2`, `tuic`) dies a few seconds after the handshake when its UDP runs through a stream-tunnelled `detour` |
| Status | I (implemented) — 2026-11-02: helper, three seams, red/green units in the tree; the final build on the owner's Mac kept h3 alive through the n009 relay in plain and XUDP packing (4 of 4 pings over 20 s, where it died at 1–5 s before); the second relay of the 2026-11-01 stand (n002) had broken on its own that day (vless TCP ping failed, QUIC through it landed on a foreign key); the reporter's device not run; hysteria2 and tuic through a vless detour verified on a loopback stand with an injected WARP-like ladder (§3): pre-fix binary dies with `short buffer`, fixed one 7 of 7 in both packings |
| Branch | `lx` |
| Build tag | — (`common/lxdatagram`, `protocol/masque` build always; the `protocol/hysteria2` and `protocol/tuic` seams sit under upstream's `with_quic`) |
| Origin | Telegram report by zomg 2026-11-01 (LxBox 2.25.19-rc.1, core `v1.14.4-lx.28`): the node "WARP (MASQUE) h3" with `detour` through free `[BL]` relays never comes up, while MASQUE h2 and AWG through the same relays work. Stand on the owner's Mac 2026-11-01 against the relays of `igareck/vpn-configs-for-russia`, decisive measurement with a debug build the same evening |
| Related | [146](../146-MASQUE_CLOSE_WEDGE_DETOUR_DEADLINE/SPEC.md) (the other half of the detour conn contract: deadlines; its wrapper sits above this one, order pinned by a test), [021](../021-MASQUE_CONNECT_IP_OUTBOUND/SPEC.md) (the outbound), [074](../074-MASQUE_VHTTP_AUTO/SPEC.md) (`auto` masked the failure by falling back to h2), the send-side MTU line [028](../028-NESTED_TUNNEL_UDP_FRAGMENT/SPEC.md) → [073](../073-CHAIN_OUTBOUND/SPEC.md) §3.5 → [120](../120-PATH_MTU_ALIGN/SPEC.md) → [144](../144-MTU_ALIGN_STREAM_BOUNDARY/SPEC.md) (packet size against the tunnel below; a different defect: none of those involve a stream detour or a read error, the readers there — kernel socket, netstack — truncate silently), [091](../091-CONFIG_VALIDATION_TUIC_MASQUE/SPEC.md) (the only other `// lx:` seam in `protocol/tuic/outbound.go`) |

A kernel UDP socket silently truncates a datagram that does not fit the caller's buffer. A UDP
"socket" tunnelled through a stream `detour` (vless XUDP or plain vless UDP over WebSocket, gRPC,
XHTTP, TCP, …) does not: sing's packet readers answer with `io.ErrShortBuffer`, quic-go treats
that read error as fatal and closes the transport. Cloudflare WARP sends twelve Path-MTU probes of
1453–1462 bytes a few seconds into every h3 session, 1–10 bytes above the 1452 that quic-go
advertises, so every MASQUE h3 tunnel through such a detour dies at that moment. The fix gives the
detour conn kernel semantics: a read into a short buffer returns the truncated head of the datagram
and drops the rest, exactly what the OS does on a real socket. Upstream `hysteria2` and `tuic`
hand quic-go their detour conn through the same read path and get the same wrapper.

## 0. In plain words

Written after the owner's questions on 2026-11-02; the sections below are the formal record, this
one is the explanation.

**What happens, step by step.**

1. WARP sends a probe datagram of 1462 bytes a few seconds after the handshake.
2. The vless or XUDP stream carries it to us whole; the stream does not care about size.
3. quic-go asks the conn below it: "give me the next packet into this 1452-byte buffer".
4. sing's reader sees that a 1462-byte frame does not fit a 1452-byte buffer and, instead of cutting
   the tail, returns `io.ErrShortBuffer`.
5. quic-go receives a read error. For it a read error means "the socket is broken", so it closes
   the whole transport. The tunnel dies.

quic-go never looks at the size of the packet. It trusts what is below it, and below it there is a
stream with a different character instead of a socket.

**What "sing's reader" is.** The code in the `sing-vmess` library that unpacks the vless tunnel on
our side. When a masque outbound runs through `detour` to a vless relay, its QUIC traffic to WARP
is wrapped into the vless connection to the relay: one TCP stream on the wire, datagrams laid one
after another with a length prefix. Something on our side has to split that stream back into
datagrams. That is the reader: `vless.PacketConn.Read` in plain packing, `XUDPConn.ReadFrom` in
XUDP. It reads two bytes of length, then that many bytes of body, and hands the result to whoever
is above as one datagram. The "buffer too small" check lives there.

**Why quic-go reads into 1452 and expects truncation.** 1452 is the largest UDP payload at an
Ethernet MTU of 1500 minus the IPv6 and UDP headers. quic-go advertises it to the server as
`max_udp_payload_size` and keeps a pool of fixed-size buffers for it so that no allocation happens
per packet. By RFC 9000 the server must not send packets larger than that, except PMTUD probes,
which are built to be lost. So from quic-go's point of view a packet longer than 1452 is always
either a probe or garbage, and the right reaction is the same: it does not decrypt, it is dropped.
There is no separate "buffer too small" handling in quic-go because a socket does not need one: the
kernel cuts the tail of the datagram by POSIX `recvfrom` semantics and reports no error. Windows is
the only one that reports `WSAEMSGSIZE`, and quic-go explicitly ignores that code. Any other read
error on a UDP socket means the socket is closed or the network is gone, so `Transport.listen`
honestly closes the transport on it.

**Why sing answers with an error instead of truncating.** For sing it is not "the transport
delivered a long packet" but "the caller gave too small a buffer"; it blames the caller, not the
network. The reader must put the datagram into the caller's buffer whole, cutting it would hand
corrupted data upwards and hide that. So it uses the standard Go answer to "buffer too small":
`io.ErrShortBuffer`, let the caller bring a bigger buffer. In sing's own world this never fires:
every caller there reads through a `buf.Buffer` of 65535 bytes, the maximum datagram size. Nobody
ever asked sing for the "behave like a kernel socket, cut silently" contract, because the situation
never came up. There is a second reason: reading the body into a temporary buffer and copying, for
a case that does not happen, is an extra copy per datagram, and sing is written for minimal copies.

**Where they collide.** quic-go thinks there is a socket below it; through `detour` there is a sing
stream. Neither side is wrong on its own, the defect is in the joint, and the joint is ours.
Upstream sing-box glues hysteria2 and tuic to a stream `detour` the same way, with the same defect,
but detouring QUIC protocols through a stream proxy is rare and not every server probes, so nobody
hit it. We hit it because WARP always probes and every `[BL]` relay is a stream.

**Why h2 and AWG through the same relays worked.** h2 runs over TCP, no datagrams and no probes at
all. AWG reads into a full-MTU buffer and survives the loss of one frame. Only QUIC broke, because
only quic-go closes the connection on a read error. hysteria2 and tuic sit on the same quic-go, so
through `detour` they carried the same disease and got the same wrapper.

**How the shim fixes it.** It stands between the two and speaks each one's language. quic-go calls
`Read` with its 1452-byte buffer. The shim puts that buffer aside and calls `Read` on sing's reader
with its own 65535-byte buffer, an ordinary call for sing, any frame fits, no error. Having the
frame, the shim copies into quic-go's buffer as much as fits, the first 1452 bytes; the 10-byte tail
stays in the shim's buffer and is overwritten by the next read. quic-go gets 1452 bytes without an
error, as from a socket, fails to decrypt and drops the packet. The probe is "lost", the server
stays at 1452. Ordinary packets shorter than 1452 pass through unchanged, with one extra copy. The
direct path without `detour` is not touched: the shim checks that a real kernel UDP socket is below
it and then does not install itself.

**Why truncate rather than drop oversize datagrams.** For WARP the outcome would be the same, the
probe disappears, but truncation was chosen for three reasons. It is an exact copy of what the
kernel does, and the shim's only job is to make the stream indistinguishable from a UDP socket;
copying the kernel literally removes the need to guess whether quic-go has a case where the stump
matters. The shim does not know what is inside: it sits below QUIC and sees bytes; dropping would
mean the shim decides for the protocol which packets it does not need, while a stump reaches the
one who can read its header and decide. And dropping is awkward at this level: `Read` must return
something, so skipping a packet means looping until a fitting one arrives, a second behaviour rule
with its own waiting logic; truncation is one `copy`.

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
- Loopback stand for hysteria2/tuic (Mac, 2026-11-02, no network): one `sing-box` process serves
  a `vless` inbound (the "relay"), a `hysteria2` inbound and a `tuic` inbound with a self-signed
  certificate; a small Python UDP proxy sits between the relay's egress and the two QUIC servers
  and, after the fourth server→client datagram of every session, injects the WARP ladder of twelve
  oversize datagrams (3×1462, 3×1457, 3×1454, 3×1453, first byte `0x40`, random body); the client
  process routes two `mixed` inbounds to a `hysteria2` and a `tuic` outbound, both with `detour` to
  the vless outbound, `packet_encoding` `xudp` and `packetaddr` in turn; seven `curl` rounds through
  each, 3 s apart, to a local HTTP server. Pre-fix binary (built from the parent of the fix
  commit): hysteria2 fails every round in both packings with
  `http3: parsing frame failed: quic: transport closed: short buffer` and re-dials a session per
  request; tuic answers the first one or two requests before the ladder lands and then times out
  every round. Fixed binary: 7 of 7 `200` for hysteria2 and tuic in both packings, one session
  per outbound for the whole run, and the one-shot debug line
  `datagram of 1462 bytes truncated to the 1452-byte read buffer` once per outbound. The stand
  reproduces the mechanism, not a live hysteria2/tuic server with its own PMTUD.
- Relay matrix re-run on the fixed binary (Mac, 2026-11-02 16:00, the same fourteen `[BL]`
  candidates as the 2026-11-01 stand, h2 and h3 to WARP through each, h3 in XUDP and native vless
  UDP packing): h3 comes up and stays up only through n009 (Poland, REALITY), the one relay that
  passes UDP untouched — 7 of 7 pings at 3-second intervals in both packings (341–460 ms XUDP,
  367–481 ms native after a 1.2 s first), with the one-shot debug line `datagram of 1462 bytes
  truncated to the 1452-byte read buffer` right after `h3 tunnel established`, i.e. the WARP ladder
  did arrive and was absorbed. No `short buffer` anywhere in the run. The other thirteen fail in
  their own layers, unchanged from §1.1: six Anycast ws fronts close the WebSocket or EOF on the
  first UDP frame (no UDP), n011 and n002 land on a foreign key (Xray QUIC sniffing by SNI), n001
  times out on its own TCP, n014/n058/n059/n085 EOF on dial. h2 passes through n009, n011, n001,
  n002. `packet_encoding: packetaddr` is a sing-box-only encoding and EOFs on every Xray relay; the
  two packings that matter in the field are XUDP (LxBox default) and native.
- Red/green on the reporter's exact path with a local relay (Mac on the direct RU uplink
  93.100.173.230, 2026-11-02 16:19): a `vless` inbound of our own `sing-box` as the relay, masque h3
  to 162.159.198.2 with `detour` through it, seven requests 3 s apart through a `mixed` inbound,
  pre-fix binary (parent of 6d80663b9) against the fixed one, XUDP and `packetaddr` packing (the
  relay is sing-box, so `packetaddr` is valid here). Pre-fix: every request dials a fresh tunnel
  and the tunnel dies within the same second as `h3 tunnel established` — 6 established / 6 died in
  XUDP, 4 / 4 in packetaddr, plus one `PROTOCOL_VIOLATION` and one stateless reset on the WARP side;
  a request still gets its `204` because it completes before the ladder lands, which is why a
  single ping never showed this. Fixed: 1 established / 0 died in both packings, 7 of 7, and the
  one-shot `datagram of 1462 bytes truncated to the 1452-byte read buffer` line once per run.
  Behind the router's tunnel path (NL egress) the same stand gives `remote endpoint has a different
  public key than pinned` for h2 and h3 alike, with or without the relay — the path, not the fix.

### 3.1 The Xray sniffing layer, taken apart (2026-11-02, the owner's own Xray in NL)

Not this spec's defect, recorded here because it is the other reason "h3 through a relay does not
reach WARP", and because the stand showed why h2 survives it. The owner's router egressed through
the owner's own Xray in Amsterdam; the Mac behind it, no detour, `sing-box-new`:

| Masque node | Result |
|---|---|
| h3, `www.cloudflare.com` SNI | `remote endpoint has a different public key than pinned` |
| h2, same SNI, whole ClientHello | the same foreign key |
| h2, same SNI, `tls.record_fragment: true` | tunnel established, 204 three of three |

Three `curl --resolve` connections to `162.159.198.2:443` through the same path explain the key:
SNI `www.cloudflare.com` returns the certificate of the real `www.cloudflare.com` (Let's Encrypt,
HTTP 200), SNI `consumer-masque.cloudflareclient.com` (no A record) and a nonexistent name are
reset. That is Xray `sniffing` with `destOverride` and `routeOnly: false`: the inbound reads the
SNI out of the TLS or QUIC ClientHello, replaces the destination with the name and resolves it
again. A resolvable name lands on that name's host and its key; an unresolvable one is dropped.
No SNI on our side helps; the only name that resolves inside `cloudflareclient.com`,
`engage.cloudflareclient.com`, points at the WireGuard endpoint `162.159.192.1`, which does not
serve MASQUE (h3 `handshake failure`, h2 a generic edge key).

Why h2 passes and h3 does not, by Xray's code (`app/dispatcher/default.go`,
`common/protocol/quic/sniff.go`, `common/protocol/tls/sniff.go`): the sniff loop has a 200 ms
budget; the TLS sniffer answers a partial ClientHello with `ErrNoClue`, which counts as an attempt,
and the loop gives up after two, so a ClientHello split into TLS records (SPEC 060 does it under any
`detour`, hence every relay user's h2 worked) is never parsed and the destination stays. The QUIC
sniffer decrypts the Initial packets with the version's initial secret, reassembles CRYPTO frames
across packets and answers `ErrProtoNeedMoreData`, which does not count as an attempt: it keeps
reading until the 200 ms run out. A QUIC ClientHello, whole or split, is therefore parsed as long
as all its Initial packets arrive within 200 ms, which they do.

Relay-side cure: `"routeOnly": true` in the inbound's `sniffing` (the sniffed domain is used for
routing only, the destination stays the IP). A client-side counter for h3 exists in principle,
the second Initial packet held back past the 200 ms budget, and is not part of this spec: a new
behaviour under the owner's §3.1 test. The same sniffer explains why upstream hysteria2 and tuic
with a fake SNI (`addons.mozilla.org` in #37) cannot pass such a relay either.

### 3.2 Prototype: holding the second Initial past the relay sniff budget (2026-11-02)

Superseded by [SPEC 151](../151-QUIC_SNIFF_DECOY/SPEC.md): a one-byte decoy datagram ahead of the Initial takes Xray's QUIC sniffer out of the flow with no delay; the prototype below is removed from the tree. Research, kept so the result is not lost. Follows from §3.1: Xray's
QUIC sniffer reassembles the ClientHello across Initial packets and answers `ErrProtoNeedMoreData`,
which does not count against its two-attempt budget, so it keeps reading until its 200 ms timer
runs out. If the second Initial datagram of a fresh h3 dial arrives after that timer, the sniffer
gives up and forwards the cached first packet to the original destination IP; the tunnel reaches
WARP with its real key.

Prototype (`protocol/masque/initial_delay_lx.go`, one marked line before `quic.DialEarly`):
a write wrapper over the QUIC conn that, for QUIC v1 long-header Initial datagrams, lets the first
through and sleeps the rest of the window on each later one. Off unless `LX_H3_INITIAL_DELAY` holds
a positive duration; no config key yet (the form is the owner's §3.1 call).

Stand (`scratchpad/xr/`): a local Xray `v26.10.31` vless inbound with `sniffing` `destOverride`
`["http","tls","quic"]`, `routeOnly: false`; a masque h3 node with `detour` through it; the NL
egress with `routeOnly: true` underneath. `run.sh` fires five `generate_204` through SOCKS5.

| Binary | Result through the local sniffing relay |
|---|---|
| plain `sing-box` (lx.30) | 0 of 5, every dial `different public key than pinned` (40 foreign-key errors over the run) |
| prototype, `LX_H3_INITIAL_DELAY=250ms` | 5 of 5, tunnel established, one held Initial per dial |

Delay sweep through the same relay:

| Hold | Result | h3 handshake |
|---|---|---|
| 150 ms | 0 of 5, still sniffed | — |
| 200 ms | 5 of 5 | 658 ms |
| 250 ms | 5 of 5 | 706 ms |
| 300 ms | 5 of 5 | 760 ms |

The threshold sits at Xray's 200 ms budget exactly; below it the relay still catches the whole
ClientHello. The cost is the hold added to the handshake, about `delay + base`.

Live `[BL]` relays (`scratchpad/xr/live.json`, the owner's NL egress underneath), plain vs
prototype at 250 ms:

| Relay | Layer | Plain | Prototype 250 ms |
|---|---|---|---|
| n009 (REALITY, no sniffing) | — | 4 of 4 | 4 of 4 |
| n002 (ws behind Cloudflare, sniffing) | foreign key | 0 of 4 | 4 of 4 |
| n011 (REALITY, sniffing) | foreign key | 0 of 4 | 4 of 4 requests, but the tunnel dies ~1 s after each — `datagram of 1462 bytes truncated` then `tunnel died: use of closed network connection` |

The delay defeats the sniffer on both relays that ran it (n002 and n011 now reach WARP with the
real key). n011 still drops the tunnel a second after each request, after the SPEC 150 truncation
line, with `use of closed network connection` — a separate death on that relay's path, not the
sniffer and not the oversize datagram (the fix truncated it); request still completes because it
finishes before the drop. Left for the owner's decision on whether to pursue (candidate: n011's
own relay closes the stream, or an interaction with idle-suspend). The main result stands: holding
the second Initial past 200 ms makes masque h3 pass an Xray sniffing relay.

## 4. Files

- `common/lxdatagram/truncate.go` — `TruncateReads`, `Dialer` (new)
- `common/lxdatagram/truncate_test.go` (new)
- `protocol/masque/outbound.go` — `connectH3WithBudget` (`// lx: SPEC 150`, one line before the SPEC 146 block)
- `protocol/masque/oversize_datagram_lx_test.go` (new)
- `protocol/hysteria2/outbound.go` — `Dialer:` in `hysteria2.NewClient` (`// lx: SPEC 150`)
- `protocol/tuic/outbound.go` — `Dialer:` in `tuic.NewClient` (`// lx: SPEC 150`)
- `.github/workflows/lx-ci.yml` — test list
