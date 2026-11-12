# SPEC: 151 — QUIC_SNIFF_DECOY

> 🌐 Русская версия: **[SPEC.ru.md](SPEC.ru.md)** (auxiliary copy; this English file is primary, both are edited together).

**Feature:** [009-MASQUE_WARP](../../FEATURES/009-MASQUE_WARP/FEATURE.md) (origin and the main path); [003-AWG](../../FEATURES/003-AWG/FEATURE.md) (the `wireguard` consumer, formerly SPEC 153); `hysteria2`, `tuic`, `trusttunnel` and XHTTP-h3 share the same helper.

| Field | Value |
|-------|-------|
| Type | F (feature) — a QUIC client passes a relay that sniffs UDP and rewrites the destination by the SNI |
| Status | I (implemented) — 2026-11-07: `lx.quic_sniff_decoy` is an object (`mode`, `outbound_type`, `detour_type`, `send`), the string and boolean forms are a load error, `lx.awg_masquerade` is removed (load error); the decoy goes on the retry after a failed first attempt (`send: retry`, default), the relay type is read at dial time from the member a group actually picked; consumers: `masque` h3, `hysteria2`, `tuic`, `trusttunnel` h3, XHTTP h3 under `vless`/`vmess`/`trojan`, `wireguard` (AmneziaWG masquerade burst). Owner's decisions 2026-11-06/07. Field runs on the object form: none yet; the string-form runs of lx.30–lx.32 are kept in §6 |
| Branch | `lx` |
| Build tag | — (`common/lxdatagram`, `protocol/masque`, `adapter`, `common/dialer` build always; the `hysteria2`/`tuic` seam is SPEC 150's line under upstream's `with_quic`; `trusttunnel` under `with_trusttunnel`; XHTTP under `with_xhttp`; AWG under `with_awg`) |
| Origin | SPEC 150 §3.1 (Xray sniffing explains why h3 through `[BL]` relays lands on a foreign key) and §3.2 (prototype that held the second Initial past Xray's 200 ms sniff budget). Reading Xray's dispatcher for that prototype showed a cheaper lever: the first datagram decides. 2026-11-06: a field report (LxBox v2.25.20, core lx.32: masque h3 through `socks` with UDP-over-TCP over an SSH tunnel to a VPS, no sniffer anywhere, `detour` mode written by LxBox) — the tunnel comes up only with the decoy off. The byte was going out wherever no sniffer was, and the client cannot know whether there is one; hence retry-first and the two type lists |
| Related | [150](../150-QUIC_DETOUR_OVERSIZE_DATAGRAM/SPEC.md) (same package, same seams; §3.1/§3.2 the analysis and the hold prototype this spec replaces), [153](../153-AWG_MASQUERADE_OVER_DETOUR/SPEC.md) (the AWG case; its key `lx.awg_masquerade` is removed by this spec, the burst filter stays as the AWG seam), [152](../152-TUNNEL_SESSION_INTERNAL_UNDERLAY/SPEC.md) (the session dialer the seams wrap), [060](../060-TLS_FRAGMENT_AUTO_ON_DETOUR/SPEC.md) (why h2 already passed the same relays) |

A relay that sniffs UDP to rewrite the destination by the SNI (Xray `sniffing` with `destOverride`
and `routeOnly: false`) makes up its mind on the first datagram of a flow. A first datagram that
is not a QUIC long-header packet takes the QUIC sniffer out of the flow; the ClientHello that
follows is never parsed and the flow goes to the address the client dialled. We send one byte,
`0x00`, before the first Initial of a QUIC dial. QUIC servers discard it unparsed.

The client cannot see whether a relay sniffs. So the byte is not sent on the first attempt: the
first dial goes clean, and only a dial that failed is retried with the byte in front. Which
outbounds do this, through which relay types and on which paths is the object `lx.quic_sniff_decoy`
(§4); absent, nothing is ever sent.

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

### 3.1 Policy — `common/lxdatagram/decoy.go`

- `sniffDecoy = []byte{0x00}`; `WriteSniffDecoy(conn, logger)` writes it once, a failed write is
  logged at debug and ignored (the decoy is advisory, the handshake reports a broken conn by itself).
- `SniffDecoyPolicy` is the resolved key (`option.LXResolved.QUICSniffDecoy`, read once per
  outbound through `SniffDecoyFromContext(ctx)`): `Mode`, the `OutboundTypes` and `DetourTypes` sets,
  `Retry`. `policy.Enabled(outboundType)` gates the whole machinery for a consumer: `mode: off` or a
  type outside `outbound_type` means no byte, no retry memory, no carrier record.
- `policy.Applies(path)` decides per dial from the path as dialled: `direct` — a kernel socket
  (`IsKernelSocket`, `common/lxdatagram/kernel.go`, the check SPEC 150 shares; no `detour`, a
  `detour` to a `direct` outbound, or a `detour` to a group whose current member is `direct`);
  `detour` — a conn carried by another outbound **whose type is in `detour_type`**; `all` — both.
  A carried conn whose carrier type is unknown (no record in the context) counts as not matching.

### 3.2 The carrier type — `adapter/carrier_lx.go`

The relay that sniffs is the nearest hop that unpacks our datagram: the outbound `detour` names, or,
when `detour` is a group, the member the group picked for this very dial. The dialled conn does not
say who carried it, so the consumer puts a `CarrierRecord` into the dial context
(`adapter.ContextWithCarrierRecord`) and two existing lx seams fill it in with
`adapter.RecordCarrier(ctx, outbound)`:

- `DetourDialer.DialContext` (`common/dialer/detour.go`, inside the SPEC 139 block) records the
  outbound `detour` resolves to;
- `adapter.ResolveChainLeaf` (SPEC 073's hook that every group calls with the member it picked)
  records that member.

A group is never recorded, so nested groups resolve down to the member; the first non-group write
wins, so the member's own `detour` (the second hop) does not overwrite the nearest hop. Chain
outbounds (SPEC 073) pass through the same hook with the link that carries the original tag and
type. The record costs one context value per dial and only exists when the consumer's policy is
enabled.

### 3.3 Retry memory — `lxdatagram.SniffDecoy`

One `SniffDecoy` per consumer (outbound or endpoint), nil when the policy is off for its type.
`Begin(ctx)` returns an `Attempt` with the carrier record in its context; `attempt.Dialed(conn,
writeTo)` classifies the path, decides whether this attempt carries the byte and writes it on
`writeTo` (the conn the QUIC stack will write to, after the consumer's own wrappers);
`attempt.Done(err)` records the outcome and reports whether the consumer should redo the dial right
now.

Per path (`direct`, or `detour:<type>`) the memory is a three-state machine:

| State | This attempt | Succeeded | Failed |
|---|---|---|---|
| `clean` (initial) | no byte | stays `clean` | → `armed` |
| `armed` | byte | → `decoy` | → `clean` |
| `decoy` | byte | stays `decoy` | → `clean` |

A failure with the byte goes back to `clean`: a node that was merely down must not pin the byte on a
path where the byte itself is what breaks the flow (the 2026-11-06 report). A different path (the
group switched from `direct` to a relay, or to a relay of another type) starts from `clean`; the
memory lives with the consumer and dies with it (outbound rebuilt, box restarted). With `send:
always` there is no memory: every applicable dial carries the byte, as lx.30–lx.32 did.

`Done(err)` asks for an immediate redo when the attempt was clean, the policy applies to the path,
the error is not a timeout (`net.Error.Timeout()`, `context.DeadlineExceeded`) and the caller's
context is still alive. A fast failure is the sniffer's usual signature (masque: `remote endpoint
has a different public key than pinned` within one RTT); a silent relay runs out the caller's
deadline, and the retry with the byte is the next dial, from `armed`.

### 3.4 Seams

| Consumer | File | Dial and handshake | Retry |
|---|---|---|---|
| `masque` h3 | `protocol/masque/outbound.go` `connectH3WithBudget` | ours; the byte goes after the SPEC 150/146 wrappers, before `quic.DialEarly` | in-call on a fast error, else next dial. In `auto` the redo happens before the h2 fallback; a silent h3 falls back to h2 as before and the next h3 leg is `armed` |
| `hysteria2`, `tuic` | `common/lxdatagram/truncate.go` `truncDialer` (SPEC 150 seam) | inside `sing-quic`, the result is not visible | indirect: the conn the dialer handed out is wrapped; a conn closed less than 30 s after its dial counts as a failed attempt, one that lived longer or is still open as a success, judged at the next dial. A false positive costs one byte on the next dial. Port hopping dials through `ListenPacket` and is outside |
| `trusttunnel` h3 | `transport/trusttunnel/client_quic.go` `connectQUIC` | ours, only with the node's `quic: true` | as masque |
| XHTTP h3 (`vless`, `vmess`, `trojan`) | `transport/v2rayxhttp/http3.go` `Dial` of the `http3.Transport` | ours, only when the transport negotiates h3 | as masque; the `outbound_type` checked is the node's (`vless`, `vmess` or `trojan`), passed into the transport at construction |
| `wireguard` (AmneziaWG) | `transport/wireguard/handshake_burst_lx.go` (SPEC 153's filter), `client_bind.go` | the bind records the carrier type and the kernel check on each socket generation | none: handshake retries reuse the socket, so the same flow — the relay decided on its first datagram already. The byte precedes a QUIC-shaped burst (`ip: quic`, or a static `i1` from a QUIC capture) on every handshake the policy applies to; `send` does not apply |

`lx.awg_masquerade` (SPEC 153) is removed with its filter branch: the masquerade burst is sent on
every path, the way the default `all` did since lx.32. The key in a config is a load error (§4).

### 3.5 What the retry does not catch

- **AmneziaWG**: there is no error, only an unanswered handshake, indistinguishable from a dead
  server. Hence no retry there, the byte goes first.
- **A handshake that lands elsewhere without an error**: `tuic` with `insecure` and an SNI that
  resolves to a live QUIC host completes the QUIC handshake there; the failure shows up later as a
  dead connection, which the 30 s window of §3.4 catches only if the server drops it quickly.
  `masque` is protected by key pinning, `hysteria2` by the authentication round trip.
- **A silent relay** (unresolvable SNI): the first dial runs out its deadline. The first URL test of
  such a node fails, the second passes; in `auto` masque falls back to h2 on that bring-up.
- **A relay that sniffs TLS on h2** is outside this spec, the decoy is for QUIC only.

### 3.6 Removed

The SPEC 150 §3.2 hold prototype (`protocol/masque/initial_delay_lx.go`) stays removed; the §3.3
fallback of the previous revision (a decoy Initial with its own connection IDs) stays on paper: no
sniffer that skips leading junk is known.

## 4. Config

Owner's decisions 2026-11-02 (one key in the root `lx` block, SPEC 098) and 2026-11-06/07 (object
form only, retry first, type lists, `awg_masquerade` removed):

```jsonc
"lx": {
  "quic_sniff_decoy": {
    "mode": "detour",             // off (default) | direct | detour | all
    "outbound_type": ["masque"],  // default: masque
    "detour_type": ["vless"],     // default: vless
    "send": "retry"               // retry (default) | always
  }
}
```

| Key | Values | Absent | Meaning |
|---|---|---|---|
| `mode` | `off`, `direct`, `detour`, `all` | `off` | On which paths the byte is allowed: `direct` — the UDP socket is a kernel socket (no `detour`, `detour` to `direct`, or to a group currently on `direct`); `detour` — the UDP is carried by another outbound of a type in `detour_type`; `all` — both. `off` sends nothing anywhere |
| `outbound_type` | `masque`, `hysteria2`, `tuic`, `trusttunnel`, `vless`, `vmess`, `trojan`, `wireguard` | `["masque"]` | Which outbounds send the byte. The type selects the node; the byte goes only where the node actually opens QUIC: `masque` on h3, `trusttunnel` with `quic: true`, `vless`/`vmess`/`trojan` with XHTTP on h3, `wireguard` before a QUIC-shaped AmneziaWG burst |
| `detour_type` | any outbound type except the groups (`selector`, `urltest`) | `["vless"]` | Through which relays. The relay is the nearest hop that carries the datagram, read at dial time: the outbound `detour` names, or the member a group picked for this dial |
| `send` | `retry`, `always` | `retry` | `retry` — the first attempt is clean, a failed one is retried with the byte (§3.3); `always` — every applicable dial carries the byte, the lx.30–lx.32 behaviour. Does not apply to `wireguard` |

Load errors, all naming the key: a string or boolean value (`lx.quic_sniff_decoy must be an
object: {mode, outbound_type, detour_type, send}`), an unknown `mode` or `send`, a type outside the
list above in `outbound_type`, a group type in `detour_type`, an
unknown key in the object, a `detour_type` no outbound or endpoint registry of the build knows
(checked at box start in `box_lx.go`, so `sing-box check` catches it; skipped with `mode: off`), and `lx.awg_masquerade` with any value (`lx.awg_masquerade is removed:
the AmneziaWG masquerade burst is sent on every path`).

The object with the four defaults and `mode: off` is canonicalised to an absent key. There is no
environment override. A config written by LxBox 2.25.x (`"quic_sniff_decoy": "detour"`,
`awg_masquerade`) does not load on this core: the LxBox core-pin bump goes together with its config
writer (owner's decision 2026-11-07, overriding the "old configs start" constraint of the task
brief). User docs — `docs-lx/lx-config.md` §13.

## 5. Verification — object form

Units:

- `option/lx_test.go` — the object resolves with its defaults, every default is canonicalised, the
  string and boolean forms and `awg_masquerade` fail with their messages, unknown types and group
  types are rejected, the schema is a strict object.
- `common/lxdatagram/decoy_test.go` — the three-state memory per path; `Done` asks for a redo on a
  fast error only; `Applies` against a kernel socket, a carried conn with a matching, a
  non-matching and a missing carrier type; `send: always` sends on every applicable dial; the
  `truncDialer` window: a conn closed young arms the next dial, a long-lived one does not.
- `adapter/carrier_lx_test.go` — first non-group write wins, groups are skipped.
- `transport/wireguard/handshake_burst_lx_test.go` — the byte precedes a QUIC-shaped burst by
  policy, never a dns/stun/sip burst; no stripping remains; the bind reports the carrier type.
- The redo loops of masque, trusttunnel and XHTTP wrap a real QUIC dial and have no unit; the
  loop is the same four lines in each seam and the verdict comes from `Attempt.Done`, which is
  covered above.

Stands: pending. The loopback stand of §6 (Xray `v26.10.31` with `destOverride` and no `routeOnly`)
is the reference: without a sniffer the byte is never seen on the wire; with one, the first dial
fails and the second carries the byte.

## 6. Field and stand runs of the string-form key (lx.30–lx.32)

Kept for the numbers: the mechanism is unchanged, the key form and the first-attempt rule are not. "With decoy" below means the byte on every dial (`send: always` today).

### 6.0 Stand 2026-11-02

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

### 6.1 Live `[BL]` relays (2026-11-02, rc.3 build)

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

### 6.2 Not covered: AWG `ip: quic` masquerade through a sniffing relay (2026-11-03)

The same sniffer breaks a WireGuard/AmneziaWG endpoint whose masquerade sends a QUIC decoy (`ip: quic`, SNI from `id`) when its `detour` is a relay running Xray `sniffing` with `quic` in `destOverride` and no `routeOnly`.

- Field case (zomg, LxBox 2.25.19-rc.2-dev.38 on `1.14.4-lx.30-rc.1`, two dumps 2026-11-03): the node to the reporter's own WireGuard server — `jc 4/40/70`, `id: cdn.jsdelivr.net`, `ip: quic`, `ib: chrome`, `detour` through the `[BL]` relay selector — fails every URL test with `context deadline exceeded`; plain WireGuard to WARP through the same selector passes (391 and 440 ms).
- Mechanism: the masquerade's QUIC Initial is the first datagram of the flow. Xray's QUIC sniffer decrypts it, takes the SNI from `id` and sends the flow to `<id>:<port>`; the WireGuard handshake never reaches the server. Plain WireGuard and AWG junk (`jc`) are not recognised by any sniffer and pass.
- Loopback stand on lx.30 (sing-box WireGuard server, client through vless, Xray `v26.10.31` relay with `destOverride` `http`/`tls`/`quic`): plain WireGuard 3/3, AWG `jc` 3/3, AWG `ip: quic` + `id` 0/3 in XUDP and plain packing, the same without the relay 3/3; through a sing-box vless relay every variant 3/3. Xray's log names the rewritten destination: `failed to open connection to udp:nonexistent.invalid:51820` with `id: nonexistent.invalid`.
- The 2026-11-03 decision was to leave it to the config (drop `id`/`ip`/`ib` on a detour node, `routeOnly: true` on the relay). Two independent field reports the same day (issue #64) turned it into [SPEC 153](../153-AWG_MASQUERADE_OVER_DETOUR/SPEC.md): `lx.awg_masquerade: "direct"` (default `all`) sends no masquerade burst over a detour at all, and `lx.quic_sniff_decoy` now reaches the AmneziaWG handshake — the byte precedes a QUIC-shaped burst on the paths the mode selects.

## 7. Files

- `common/lxdatagram/decoy.go`, `decoy_test.go` — policy, `SniffDecoy`/`Attempt`, the retry memory
- `common/lxdatagram/truncate.go` — `truncDialer` wraps the conn and judges the previous dial
- `common/lxdatagram/kernel.go`, `kernel_test.go` — `IsKernelSocket`, `IsKernelPacketConn`
- `adapter/carrier_lx.go`, `carrier_lx_test.go` (new) — `CarrierRecord`
- `adapter/chain_lx.go` — `ResolveChainLeaf` records the picked member (`// lx: SPEC 151`)
- `common/dialer/detour.go` — the SPEC 139 block records the detour outbound (`// lx: SPEC 151`)
- `box_lx.go`, `box_lx_test.go` — `detour_type` against the registries
- `option/lx.go`, `option/lx_test.go` — `LXQUICSniffDecoyOptions`, `LXQUICSniffDecoyResolved`, the removed-key guard for `awg_masquerade`
- `protocol/masque/outbound.go` — `connectH3WithBudget` with the attempt loop
- `protocol/hysteria2/outbound.go`, `protocol/tuic/outbound.go` — the SPEC 150 line (unchanged)
- `transport/trusttunnel/client_quic.go`, `transport/v2rayxhttp/http3.go` — new seams
- `transport/wireguard/handshake_burst_lx.go`, `client_bind.go`, `endpoint.go` — the filter by policy, the carrier type on the bind; no stripping
- `docs-lx/lx-config.md`, `docs-lx/lx-config.ru.md` — §13
