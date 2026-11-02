# SPEC: 151 — QUIC_SNIFF_DECOY

> 🌐 Русская версия: **[SPEC.ru.md](SPEC.ru.md)** (auxiliary copy; this English file is primary, both are edited together).

**Feature:** [009-MASQUE_WARP](../../FEATURES/009-MASQUE_WARP/FEATURE.md) (origin and the main path); `hysteria2` and `tuic` get the same behaviour through the SPEC 150 dialer seam, no new line in upstream files.

| Field | Value |
|-------|-------|
| Type | F (feature) — a QUIC client (`masque` h3, `hysteria2`, `tuic`) passes a relay that sniffs UDP and rewrites the destination by the SNI |
| Status | I (implemented) — key `lx.quic_sniff_decoy` with modes `off`/`direct`/`detour`/`all`, off by default (owner 2026-11-02); 2026-11-02: helper, two seams, units; stand on the owner's Mac against Xray `v26.10.31` with `destOverride` and no `routeOnly`: masque h3 → WARP 0 of 5 without the decoy (foreign key), 5 of 5 with it in XUDP and plain packing, the tunnel held 2 minutes (12 of 12 requests, 20 MB at 2.6 MB/s); hysteria2 and tuic on a loopback server 0 of 4 → 4 of 4; live `[BL]` list (14 relays, 4 alive) on the rc.3 build: h3 through the three sniffing relays 0 of 3 → 3 of 3 / 3 of 3 / 2 of 3 (§5.1) |
| Branch | `lx` |
| Build tag | — (`common/lxdatagram`, `protocol/masque` build always; the `hysteria2`/`tuic` seam is SPEC 150's line under upstream's `with_quic`) |
| Origin | SPEC 150 §3.1 (Xray sniffing explains why h3 through `[BL]` relays lands on a foreign key) and §3.2 (prototype that held the second Initial past Xray's 200 ms sniff budget). Reading Xray's dispatcher for that prototype showed a cheaper lever: the first datagram decides |
| Related | [150](../150-QUIC_DETOUR_OVERSIZE_DATAGRAM/SPEC.md) (same package, same seams; §3.1/§3.2 the analysis and the hold prototype this spec replaces), [060](../060-TLS_FRAGMENT_AUTO_ON_DETOUR/SPEC.md) (why h2 already passed the same relays) |

A relay that sniffs UDP to rewrite the destination by the SNI (Xray `sniffing` with `destOverride`
and `routeOnly: false`) makes up its mind on the first datagram of a flow. A first datagram that
is not a QUIC long-header packet takes the QUIC sniffer out of the flow; the ClientHello that
follows is never parsed and the flow goes to the address the client dialled. We send one byte,
`0x00`, on every fresh QUIC dial before the first Initial. QUIC servers discard it unparsed.
No delay, no retries, no per-node state.

## 1. Problem

Through a relay with Xray `sniffing` `destOverride: ["quic", …]` and no `routeOnly`, the relay
decrypts the client's Initial packets (their keys derive from a public salt and the DCID),
reassembles the ClientHello, takes the SNI and replaces the destination with that name. A
resolvable SNI lands on that name's host (masque: `remote endpoint has a different public key
than pinned`; hysteria2/tuic with a fake SNI: someone else's server or nothing), an unresolvable
one is dropped. The relay is not ours, the user often cannot change it (`[BL]` relays, SPEC 150
§1.1); h2 passed only thanks to SPEC 060's record fragmentation, h3, hysteria2 and tuic never did.

## 2. Why the first datagram decides

Xray `app/dispatcher/default.go` `sniffer()` and `app/dispatcher/sniffer.go` `Sniffer.Sniff`
(`v26.10.31`):

- The dispatcher caches everything read from the flow and calls `Sniff` on the whole cache each
  round, within a 200 ms budget and two "no clue" attempts.
- `Sniff` runs every sniffer of the network. A sniffer that answers `ErrNoClue` stays in the list
  for the next round; `ErrProtoNeedMoreData` narrows the list to that sniffer and keeps reading;
  **any other error drops the sniffer from the list for good**. If none is left, `Sniff` returns
  `errUnknownContent` and the dispatcher stops sniffing at once and routes the original address.
- `SniffQUIC` parses the cache from its first byte. A first byte without the long-header bit
  (0x80) or the fixed bit (0x40) returns `errNotQUICInitial` on the spot. A long-header packet of
  another type (Handshake, 0-RTT) is **skipped**, and an exhausted cache answers
  `ErrProtoNeedMoreData` — so a Handshake-shaped decoy would make the sniffer wait for the real
  Initial and parse it; the decoy must fail the very first check.

So a first datagram `0x00` makes `SniffQUIC` answer `errNotQUICInitial`, the QUIC sniffer leaves
the flow before the Initial arrives, and the uTP sniffer that may remain does not recognise QUIC.
The order inside one cache read does not matter: the cache starts with the decoy either way.

On the server side the decoy is inert: RFC 9000 §17 requires the fixed bit, quic-go
(`wire.IsPotentialQUICPacket`) and other stacks drop such a datagram before any connection
lookup, and one byte is far below the smallest packet that can draw a stateless reset (21 bytes).
hysteria2 with Salamander drops it on deobfuscation (too short for the salt).

sing-box as a relay: the current line (1.13+) has no destination override by sniffing at all
(`sniff_override_destination` removed upstream in `8ae93a98e`); the sniffed domain only feeds
routing, so a sing-box relay does not break these protocols and needs no countermeasure. Its
packet sniff also decides on the first packet (`route/route.go`, "packet sniff skipped due to
previous error"), so the decoy would hold there too.

## 3. Implementation

### 3.1 `common/lxdatagram/decoy.go`

- `sniffDecoy = []byte{0x00}`.
- `WriteSniffDecoy(conn, logger)` writes it once; a failed write is logged at debug and ignored
  (the decoy is advisory, the handshake reports a broken conn by itself).
- `SniffDecoyFromContext(ctx)` returns the `lx.quic_sniff_decoy` mode from the box context
  (`option.LXResolved.QUICSniffDecoyOrOff`, absent = `off`). No environment override (§4).
- `SniffDecoyApplies(mode, conn)` decides per dial by what lies under the QUIC stack, on the conn
  as dialled, before any wrapper: `direct` — a kernel socket (the SPEC 150 check: `syscall.Conn`
  once replaceable readers are unwrapped); `detour` — anything else, a conn carried by another
  outbound (proxy or tunnel); `all` — both; `off` — neither. A `detour` to a `direct` outbound
  yields a kernel socket and counts as `direct`.

### 3.2 Seams

- `masque` — `NewOutbound` reads the mode once into `Outbound.sniffDecoy`;
  `connectH3WithBudget` (`// lx: SPEC 151`) checks it against the dialled `udpConn` and sends the
  decoy after the SPEC 150/146 wrappers and before `quic.DialEarly`, on the conn quic-go will
  write to. A sniffing relay may sit upstream of a kernel socket too (SPEC 150 §3.1: the owner's
  router egressing through the owner's own Xray) — that is the `direct` case.
- `hysteria2`, `tuic` — `lxdatagram.Dialer(ctx, …)` (SPEC 150's seam, now taking the box context
  for the mode) checks each `udp` `DialContext` result and sends the decoy before handing the conn
  to `sing-quic`; `tcp` and `ListenPacket` are untouched.
  hysteria2 port hopping dials through `ListenPacket` and gets no decoy (out of scope).

The prototype of SPEC 150 §3.2 (`protocol/masque/initial_delay_lx.go`, `LX_H3_INITIAL_DELAY`)
is removed: it cost the hold on every handshake (≈ +200 ms), needed a ClientHello spread over
two Initials (a single-Initial ClientHello leaves nothing to hold and would need the Initial
re-packetised, which collides with quic-go's packet-number space), and needed an adaptive ladder
because the budget differs between relays. The decoy needs none of that.

### 3.3 Fallback kept on paper

If a sniffer appears that skips leading junk, the next step is a timed hold behind a decoy
Initial with its own random connection IDs and a ClientHello prefix (Xray decrypts each packet
with keys from that packet's DCID and assembles CRYPTO by offset), real Initials held past the
budget — no packet-number rewriting of quic-go's packets. Not implemented: no such sniffer known.

## 4. Config

Owner's decision 2026-11-02: one key in the root `lx` block (SPEC 098), next to `mtu_align`,
global for the three clients; absent = `off`. Modes split the direct path from detours, so that a
DPI that reacts to the leading byte on the user's own uplink can be kept away from it while relays
still get the decoy:

| Value | Decoy on |
|---|---|
| `"off"` / `false` / absent | nothing |
| `"direct"` | dials whose UDP is a kernel socket (no `detour`, or `detour` to a `direct` outbound) |
| `"detour"` | dials whose UDP is carried by another outbound (proxy or tunnel) |
| `"all"` / `true` | every dial |

```json
"lx": { "quic_sniff_decoy": "detour" }
```

The boolean form is what rc.3 shipped (`true` = every dial); it stays accepted and is written back
as `"all"`. An unknown string is a load error.

The key is the only switch: the `LX_QUIC_SNIFF_DECOY` environment override that rc.3 carried is
removed (owner 2026-11-02). User docs —
`docs-lx/lx-config.md` §13.

## 5. Verification

Stand on the owner's Mac (`scratchpad/xr`, `scratchpad/ht`), Xray `v26.10.31` vless inbound with
`sniffing` `destOverride: ["http","tls","quic"]`, `routeOnly: false`; Mac egress unchanged.

| Client | Path | Without decoy | With decoy |
|---|---|---|---|
| masque h3 → WARP `162.159.198.2` | vless XUDP | 0 of 5, 40 × foreign key | 5 of 5, handshake 454 ms |
| masque h3 → WARP | vless plain UDP | 0 of 5, 40 × foreign key | 5 of 5, handshake 478 ms |
| masque h3, 2-minute life | vless XUDP | — | 12 of 12 requests every 10 s, one tunnel, no deaths, 20 MB at 2.6 MB/s |
| hysteria2 → loopback server, SNI `www.cloudflare.com` | vless XUDP | 0 of 4 (Xray: `sniffed domain`, routed to `www.cloudflare.com:4443`) | 4 of 4 (`default route for udp:127.0.0.1:4443`) |
| tuic → loopback server | vless XUDP | 0 of 4 | 4 of 4 |

The loopback stand needs `finalRules: [{"action":"allow","ip":["127.0.0.0/8"]}]` on Xray's
freedom, which blocks private targets by default. For comparison, the SPEC 150 §3.2 hold
prototype passed at ≥ 200 ms with a 658–760 ms handshake.

The key itself (no env) on the same stands: absent — masque 0 of 5, hysteria2/tuic 0 of 4; `true` —
5 of 5 and 4 of 4. Modes on the xr stand: `"detour"` through the sniffing relay 5 of 5 (decoy
sent); `"direct"` through the same relay 0 of 5, foreign key (no decoy on a detour conn, as
designed); `"direct"` on the node without a detour 5 of 5 (decoy sent on the kernel socket, tunnel
up).

Units (`common/lxdatagram/decoy_test.go`, `option/lx_test.go`): the dialer sends the decoy first on
`udp` only, nothing when the key is absent or off; `direct`/`detour`/`all` against a real kernel
socket and a detour conn; every value form resolves, off canonicalizes to absent, `true` writes
back as `"all"`, unknown values are rejected; the schema is `anyOf(enum, boolean)`; the decoy is not a long header, has the fixed bit clear and is shorter than a
stateless-reset trigger.

### 5.1 Live `[BL]` relays (2026-11-02, rc.3 build)

The 14 vless relays of the 2026-11-01 stand (SPEC 150 §1.1), one masque h3 node per relay through
XUDP plus an h2 node as a liveness control; three `generate_204` delay probes per node through the
clash API, the same binary with the key absent and with `true`. Mac egress unchanged.

| Relay | h2 (control) | h3, key absent | h3, `quic_sniff_decoy: true` |
|---|---|---|---|
| n001 | 3 of 3 | 0 of 3, foreign key ×3 | 3 of 3 (431–1477 ms) |
| n002 | 2 of 3 | 0 of 3, foreign key ×3 | 3 of 3 (414–1423 ms) |
| n009 (no sniffing) | 3 of 3 | 3 of 3 | 3 of 3 |
| n011 | 3 of 3 | 0 of 3, foreign key ×3 | 2 of 3, real key; the tunnel dies after each request |
| 10 others | 0 of 3 (four of them: foreign key on h2 — TLS sniffing too) | 0 of 3 | 0 of 3 |

The decoy turns every live sniffing relay from foreign key to WARP's real key. n011's tunnel then
dies with `received a stateless reset` and `use of closed network connection`, as with the §3.2
hold prototype before: WARP no longer knows the connection a second in — consistent with the
relay sending later packets from another egress (a different anycast landing), not with the decoy,
which a server cannot answer. Not pursued here. The dead relays and the four that sniff TLS on h2
are outside this spec: the decoy is for QUIC only.

## 6. Files

- `common/lxdatagram/decoy.go`, `common/lxdatagram/decoy_test.go` (new)
- `common/lxdatagram/truncate.go` — `Dialer(ctx, …)`, `truncDialer.DialContext` sends the decoy
- `option/lx.go` — `LXQUICSniffDecoy` (modes, boolean form, schema), `LXOptions.QUICSniffDecoy`, `LXResolved.QUICSniffDecoy`; `option/lx_test.go`
- `protocol/hysteria2/outbound.go`, `protocol/tuic/outbound.go` — the SPEC 150 line passes `ctx`
- `docs-lx/lx-config.md`, `docs-lx/lx-config.ru.md` — §13 `lx.quic_sniff_decoy`
- `protocol/masque/outbound.go` — `Outbound.sniffDecoy`, `connectH3WithBudget` (`// lx: SPEC 151`)
- `protocol/masque/initial_delay_lx.go` — removed (SPEC 150 §3.2 prototype)
