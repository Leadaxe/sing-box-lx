# lxd gRPC API — observability for client tooling

> 🌐 Русская версия: **[lxd-grpc-api.ru.md](lxd-grpc-api.ru.md)**.

How a client (the launcher, a profiler, any diagnostic tool) reads live state out
of a `sing-box lxd` daemon over gRPC.

**The proto is the contract; this document explains it.** When the two disagree,
[`daemon/started_service.proto`](../daemon/started_service.proto) wins. What you
will not find in the proto — delta semantics, which fields are populated in which
event, the traps that already cost us production incidents — is what this file is
for.

For connecting to the daemon (mTLS, client certificates, admin REST) see
[lxd-daemon.md](lxd-daemon.md).

> ⚠️ **The proto file lives in `daemon/`, not in `lxd/`.** The daemon serves the
> same `StartedService` the Android line speaks, which is why anything added for
> mobile observability reaches a server for free, and vice versa.

## One contract, two transports

This observability surface is **not lxd-specific**. The exact same set of RPCs and
messages is exposed over two carriers:

| Carrier | Where | Client entry point |
|---|---|---|
| **gRPC** (`StartedService`) | the `sing-box lxd` daemon — desktop / server | `daemon/started_service.proto` |
| **libbox `CommandClient`** | the Android / iOS **AAR** (duplex conn, not gRPC) | `experimental/libbox/command_client_command_lx.go` |

The lx extensions (`SubscribeDNSQueries`, `GetRules`, `GetGroups`, `GetOutbounds`,
`GetPool`, `GetDNSGroups`, `GetRunningConfig`, `URLTestOutbound`,
`GetURLViaOutbound`, `GetChains`) are implemented **once in the core** and reached through
either carrier — the field semantics below are identical on both. So a field table
here describes the Android AAR just as much as a remote lxd machine; only the wire
framing and the method-call syntax differ. Where a rule is transport-specific it is
called out (e.g. the gRPC `Unimplemented` code has no libbox equivalent — there the
missing-tag failure surfaces as a plain error).

## Scope

`StartedService` is the data plane of the daemon: everything about the **running
core** — status, connections, DNS, groups, rules. `ManagedService` is the control
plane (stop, reload, system proxy) and is small enough to read straight from
[`daemon/managed_service.proto`](../daemon/managed_service.proto).

This document covers the observability subset. `StartedService` also carries
Tailscale, OpenVPN, OpenConnect and USB-IP RPCs — dozens of them, none related to
observing traffic. They exist, they are out of scope here; read the proto.

REST endpoints for profiling the **daemon process itself** (memory, pprof, its own
log) are a separate plane on the admin port — `/admin/memory`, `/admin/stats`,
`/admin/logs`, `/admin/pprof/*`, plus host telemetry in `/admin/host`. They are
documented in [lxd-daemon.md](lxd-daemon.md) (SPEC 065, 068).

## Build-tag gating

Nine RPCs live inside an `lx:begin lx_command` … `lx:end lx_command` block and
exist **only when the binary is built with `with_lx_command`**:

`URLTestOutbound`, `GetRules`, `GetGroups`, `GetOutbounds`, `SubscribeDNSQueries`,
`GetPool`, `GetDNSGroups`, `GetRunningConfig`, `GetURLViaOutbound`, `GetChains`.

Against a binary built without the tag they return `codes.Unimplemented`. The
generated client stubs still contain the methods — the gate is server-side, so
this is a runtime failure, not a compile error. A client that depends on the DNS
plane should probe once at startup (`GetRules` is the cheapest) and degrade
explicitly rather than surfacing `Unimplemented` per-call.

Everything else in this document — `SubscribeConnections`, `SubscribeStatus`,
`CloseConnection`, `GetStartedAt` — is ungated and present in every build.

## Rules that apply to every stream

**`interval` is nanoseconds.** Every `Subscribe*Request.interval` is a
`time.Duration` count, following the sing-box/libbox convention. Values below the
200 ms floor are clamped and logged as a warning (`minSubscribeInterval` /
`clampSubscribeInterval` in [daemon/started_service.go](../daemon/started_service.go)).

> This is not hypothetical. The launcher once sent `1000`, meaning milliseconds;
> the server read 1 µs and the ticker burned a full CPU core. Fixed in
> `fca1d367e` by adding the clamp — but a client sending `1000` still gets 200 ms,
> not the 1 s it intended. Send `int64(time.Second)`, never a bare number.

**Streams wait for the core.** Handlers call `waitForStarted` before doing
anything: subscribing while the core is starting blocks rather than failing. A
client may open its streams immediately after connecting.

**No core, no connection tracking.** If the instance has no `trafficManager`,
`SubscribeConnections` returns `Unimplemented` — distinct from the build-tag case
only by context, so do not treat `Unimplemented` as "wrong binary" without
checking whether the core is running at all.

**Stream break means your state is stale.** On `Recv` error, every connection you
remember is unconfirmed. Drop the table. Holding it shows long-dead connections as
live.

## Connection plane

### `SubscribeConnections(SubscribeConnectionsRequest) → stream ConnectionEvents`

**This is a delta protocol, not a repeated snapshot.** The single most common way
to get it wrong is to treat each `ConnectionEvents` as the current list of
connections. It is not — it is what changed.

```
ConnectionEvents { repeated ConnectionEvent events; bool reset; }
ConnectionEvent  { ConnectionEventType type; string id; Connection connection;
                   int64 uplinkDelta; int64 downlinkDelta; int64 closedAt; }
```

| `ConnectionEvent` field | Type | Meaning |
|---|---|---|
| `type` | enum | `CONNECTION_EVENT_NEW` (0) \| `CONNECTION_EVENT_UPDATE` (1) \| `CONNECTION_EVENT_CLOSED` (2) |
| `id` | string | connection UUID — the key you index by, present on every event |
| `connection` | `Connection` | full object on `NEW`, **always `nil` on `UPDATE`**, may be `nil` on `CLOSED` (see below) |
| `uplinkDelta` / `downlinkDelta` | int64 | **bytes since the last tick** (not a rate), only on `UPDATE` |
| `closedAt` | int64 | Unix **milliseconds**, only on `CLOSED` |
| `ConnectionEvents.reset` | bool | this frame replaces your whole table (see below) |

**The first frame carries `reset = true`** and contains the full current state as
a batch of `NEW` events: every active connection, plus recently closed ones
(already carrying `closedAt`). `reset` also arrives mid-stream when the core
restarts under the client's feet — an `apply`, a Start/Stop. On `reset`, clear
your table before applying the events in that same frame.

Subsequent frames arrive on two schedules: **immediately** when the core opens or
closes a connection (drained in a batch, so one frame may hold many events), and
**on the interval tick** carrying traffic deltas.

### Which fields are populated

| Event type | `connection` | `uplinkDelta` / `downlinkDelta` | `closedAt` |
|---|---|---|---|
| `NEW` | **full object** | — | — |
| `UPDATE` | **always `nil`** | the delta since the last tick | — |
| `CLOSED` | usually full, **may be `nil`** | — | set |

**`UPDATE` never carries the connection object.** Only `id` and the two deltas.
A client that reads `ev.GetConnection()` and skips the event when it is `nil` will
show connections that never accumulate any traffic. Traffic counters must be
maintained client-side by adding deltas onto the totals from the `NEW` event.

**`CLOSED` may have a `nil` connection** when the metadata is no longer
retrievable. `id` and `closedAt` are always present, so key your removal on `id`,
never on the embedded object.

**Connections can close without a close event reaching you.** If a connection
disappears from the active set between ticks and never produced a subscription
event, the server synthesizes a `CLOSED` on the next tick — with `closedAt` set to
*now*, not the real close time, and `connection` filled only if it was still in
the closed-connections ring. Treat `closedAt` as approximate.

**A zero-delta `UPDATE` is meaningful.** It marks a connection that *was*
transferring and now is not — the edge that lets a client drop a row's rate to
zero instead of leaving the last non-zero value on screen forever. Idle
connections that never moved produce no events at all.

**Counters can move backwards.** Connection IDs are reused. When the server sees a
negative delta it re-baselines and emits a zero `UPDATE` instead of a negative
one; a client will never receive a negative delta, but should not assume totals
are monotonic across an ID it has seen before.

### `Connection`

| Field | Type | Meaning |
|---|---|---|
| `id` | string | UUID; the key for `CloseConnection` and for your own table |
| `inbound`, `inboundType` | string | which inbound accepted it |
| `network` | string | `tcp` / `udp` |
| `ipVersion` | int32 | 4 or 6 |
| `source`, `destination` | string | `host:port`; destination may be an IP or a hostname |
| `domain` | string | the **sniffed** domain — empty when sniffing did not fire |
| `protocol` | string | sniffed application protocol (`http`, `tls`, `quic`, …) |
| `user` | string | inbound auth user, when the inbound has auth |
| `createdAt`, `closedAt` | int64 | Unix **milliseconds**; `closedAt` zero while open |
| `uplink`, `downlink` | int64 | current rate (bytes/s) |
| `uplinkTotal`, `downlinkTotal` | int64 | bytes accumulated by this connection |
| `rule` | string | the matched rule, rendered — pair with `GetRules` for structure |
| `outbound`, `outboundType` | string | the final outbound |
| `chainList` | repeated string | the outbound chain, final → outward |
| `detourList` | repeated string | **lx**: the final outbound's transport detour tail (SPEC 017) |
| `processInfo` | `ProcessInfo` | `processId` (uint32), `userId` (int32), `userName`, `processPath`, `packageNames` (repeated) |
| `fromOutbound` | string | set when the connection originated from an outbound itself |

**`chainList` omits the detour by design** — hence `detourList`, which the fork
adds. Order is final outbound → outward. Empty for outbounds without a detour. A
profiler that shows "the full path" needs both, concatenated.

**`domain` versus `destination`.** For a client that wants "what host is this",
prefer `domain`, fall back to the host part of `destination`. That is exactly what
the launcher does in `ProtoConnToClash`
(`singbox-launcher/internal/traffic/grpc_tracker.go`).

### Closing connections

- `CloseConnection(CloseConnectionRequest{id})` — one connection, by UUID.
- `CloseAllConnections(Empty)` — all of them.

## DNS plane

### `SubscribeDNSQueries(SubscribeDNSQueriesRequest) → stream DnsQueryEvent`

**A remote client does not need the core's text log for DNS.** This stream (SPEC
018/035) is the structured equivalent, and it carries attribution the log never
had. This matters specifically for remote machines, where the log file is on
someone else's filesystem and Clash API `/connections` is not exposed at all.

The request has a single field: `includeAnswers` (bool). Set it `true` to receive
the response records in `answers`.

| Field | Type | Meaning |
|---|---|---|
| `domain` | string | the queried name |
| `queryType` | uint32 | DNS type code of the question (1 = A, 28 = AAAA, 65 = HTTPS, …) |
| `rcode` | int32 | response code; **`-1` when there was no response at all** |
| `ttl` | uint32 | answer TTL, seconds |
| `source` | string | resolver verb: `exchanged` / `cached` / `optimistic` / `refreshed` / `failed` |
| `failed` | bool | true on timeout / SERVFAIL / rejected — failures are first-class |
| `error` | string | the failure detail, when `failed` |
| `answers` | repeated `DnsAnswer` | `{name, type (uint32), rdata, ttl (uint32)}`, **in wire order**; only when `includeAnswers` |
| `dnsServer` | string | which transport (DNS server tag) resolved it |
| `dnsServerType` | string | that server's type |
| `outbound` | repeated string | the channel(s) that server is bound to; **empty on cached/optimistic** |
| `processInfo` | `ProcessInfo` | app attribution — package / uid |
| `dnsGroupPath` | repeated string | group nesting, inside-out; empty = no group involved (SPEC 035) |
| `attempts` | repeated `DnsGroupAttempt` | probe chronology at answer time: `{server, serverType, outcome, rttMs (uint32)}` |
| `fanned` | bool | a fan-out (rescue / election / parallel) was involved |
| `survival` | bool | answer came from the least-dirty server when none was clean |

`DnsGroupAttempt.outcome` vocabulary: `answered` \| `timeout` \| `network_error` \|
`servfail` — where `answered` includes NXDOMAIN and empty answers (valid responses,
not failures).

**CNAME chains come from `answers`.** They are the response records in wire order,
CNAME hops followed by the A/AAAA records — walk them to reconstruct the chain.
There is no separate CNAME field and none is needed. The launcher does this in
`core/services/lxd_remote_transport.go` (see `dnsTypeCNAME`).

**Empty `outbound` is state, not a bug.** It means the query never left the device
— a cached or optimistic answer. Likewise an empty `dnsGroupPath` means the query
did not go through a DNS group. Check the SPEC semantics before suspecting the
core.

**`attempts` is a snapshot, not the whole truth.** Fan-out stragglers that
resolved after the answer was returned are absent by design; `GetDNSGroups` is the
full picture.

## Supporting RPCs

| RPC | Use |
|---|---|
| `SubscribeStatus(interval) → stream Status` | memory, goroutines, `connectionsIn`/`connectionsOut`, rate and totals — the header line of a profiler |
| `GetStartedAt → StartedAt` | core start time, for uptime |
| `GetRules → RuleList` | **lx**: structured rules — resolves `Connection.rule` |
| `GetGroups → Groups` / `SubscribeGroups` | **lx** (`GetGroups`) / upstream (stream) — group state |
| `GetOutbounds → OutboundList` | **lx**: outbound tags, to resolve chain entries |
| `GetPool(GetPoolRequest) → PoolList` | **lx**: rotation state of a balanced urltest group (SPEC 019) |
| `GetDNSGroups → DnsGroupList` | **lx**: full DNS group state |
| `GetRunningConfig → RunningConfig` | **lx**: the config the core is actually running |
| `URLTestOutbound → delay` | **lx**: probe a single node |
| `GetURLViaOutbound → body` | **lx**: probe returning the response body — "which exit IP does *this* node give me" (SPEC 058) |
| `GetChains → ChainList` | **lx**: state of every `chain` outbound — positions, resolved node, link state/MTU/strip/rewrite, counters (SPEC 073) |
| `SubscribeLog → stream Log` | the **core's** log, not the daemon's — see SPEC 065 for the latter |

`Status.trafficAvailable` distinguishes "zero traffic" from "traffic accounting
not available"; do not render zeros when it is false.

### Message field reference

The response shapes of the supporting RPCs, for completeness.

**`Status`** (`SubscribeStatus`) — the profiler header line:

| Field | Type | Meaning |
|---|---|---|
| `memory` | uint64 | resident bytes of the core process |
| `goroutines` | int32 | live goroutine count |
| `connectionsIn` / `connectionsOut` | int32 | active inbound / outbound connections |
| `trafficAvailable` | bool | false = accounting off; **do not render the byte fields as zero** |
| `uplink` / `downlink` | int64 | current rate, bytes/s |
| `uplinkTotal` / `downlinkTotal` | int64 | bytes since core start |

**`ServiceStatus`** (`SubscribeServiceStatus`): `status` enum
`IDLE|STARTING|STARTED|STOPPING|FATAL`, plus `errorMessage` (set on `FATAL`).
**`Version`** (`GetVersion`): `version` (string), `apiVersion` (int32).
**`StartedAt`** (`GetStartedAt`): `startedAt` (int64, Unix ms).

**`Group`** / **`GroupItem`** (`GetGroups` / `SubscribeGroups`):

| Field | Type | Meaning |
|---|---|---|
| `Group.tag`, `Group.type` | string | group tag and kind (`selector` / `urltest`) |
| `Group.selectable` | bool | whether `SelectOutbound` applies |
| `Group.selected` | string | current node — **a hint only for `round_robin`** (read `GetPool`) |
| `Group.isExpand` | bool | UI expand state |
| `Group.mode` | string | **lx**: `least_test` / `round_robin`; empty for non-urltest groups (SPEC 019) |
| `Group.items` | repeated `GroupItem` | member nodes |
| `GroupItem.tag`, `GroupItem.type` | string | node tag and type |
| `GroupItem.urlTestTime` | int64 | Unix ms of the last probe |
| `GroupItem.urlTestDelay` | int32 | last probe delay in **ms**; `0` = dead / not measured |

**`PoolSlot`** (`GetPool` → `PoolList.slots`) — **lx**, SPEC 019:

| Field | Type | Meaning |
|---|---|---|
| `slot` | uint32 | fixed slot index in the rotation |
| `tag` | string | the node currently in that slot |
| `delay` | uint32 | last test result in ms; **`0` = dead / not measured** (a live node is clamped to ≥ 1 server-side) |

A non-`round_robin` group returns an **empty `slots` list** — "no pool here", not an
error.

**`DnsGroupState`** / **`DnsGroupMember`** (`GetDNSGroups`) — **lx**, SPEC 035:

| Field | Type | Meaning |
|---|---|---|
| `DnsGroupState.tag`, `.mode`, `.current` | string | group tag, mode, sticky target |
| `DnsGroupMember.tag`, `.serverType` | string | member tag and server type |
| `DnsGroupMember.clean` | bool | zero live errors |
| `DnsGroupMember.liveErrors` | uint32 | current live error count |
| `DnsGroupMember.lastErrorAgeMs` | int64 | age of the newest live error; **`-1` = none** |
| `DnsGroupMember.liveWins` | uint32 | live win records (fastest mode) |
| `DnsGroupMember.current` | bool | is this the group's sticky target |
| `DnsGroupMember.lastRttMs` | uint32 | last successful probe; `0` = never measured |

**`URLTestOutbound`** (**lx**) — request `{outboundTag, link, timeout}`, response
`{delay (uint32, ms), error}`. `timeout` is in **milliseconds**; `0` = no extra
deadline (bounded only by the call). On failure `delay` is unset and `error` is
populated.

**`GetURLViaOutbound`** (**lx**, SPEC 058) — a diagnostic HTTP probe through one
node that returns the **response body**, answering "which exit IP / geo / warp state
does *this* node give me":

| Request field | Type | Meaning |
|---|---|---|
| `outboundTag` | string | the node to probe through |
| `link` | string | URL to fetch |
| `timeout` | uint32 | **ms**; `0` = no extra deadline |
| `maxBytes` | uint32 | body cap; `0` → **256 KiB** default, hard ceiling **1 MiB** |
| `headers` | repeated `{key, value}` | extra request headers |

| Response field | Type | Meaning |
|---|---|---|
| `httpStatus` | uint32 | HTTP status code |
| `body` | bytes | response body (**bytes**, not string — an arbitrary endpoint is not valid UTF-8) |
| `truncated` | bool | body hit `maxBytes` and was cut |
| `contentType` | string | response `Content-Type` |
| `remoteAddr` | string | the exit address the request left from |
| `elapsedMs` | uint32 | round-trip time |
| `error` | string | set when the fetch never completed (unknown tag, dial/TLS failure, timeout) |

**`RunningConfig`** (`GetRunningConfig`, **lx** SPEC 037): a single `content`
(string) — the canonical serialization of the options the core actually built from
(post-override). **Not** byte-identical to the profile text the client sent (field
order, `omitempty`, `[]`→`null`); compare semantically, not textually.

## observability-api-lx

What the fork does to this plane relative to upstream sing-box.

### Added

Nine RPCs behind `with_lx_command`, all absent upstream:

| RPC | Why | SPEC |
|---|---|---|
| `SubscribeDNSQueries` | structured DNS stream; failures first-class; replaces log parsing | [018](../SPECS/TASKS/018-DNS_QUERY_STREAM/SPEC.md), 035 |
| `GetRules` | rule table, to resolve `Connection.rule` | 014/015 |
| `GetGroups`, `GetOutbounds` | one-shot reads where upstream offers only streams | 014/015 |
| `GetPool` | rotation state of a balanced urltest group | [019](../SPECS/TASKS/019-URLTEST_MODE_STICKY/SPEC.md) |
| `GetDNSGroups` | DNS group state | 035 |
| `GetRunningConfig` | the config actually in effect | — |
| `URLTestOutbound` | probe one node, get a delay | 014/015 |
| `GetURLViaOutbound` | probe one node, get the response body | [058](../SPECS/TASKS/058-GET_URL_VIA_OUTBOUND/SPEC.md) |
| `GetChains` | state of every `chain` outbound | [073](../SPECS/TASKS/073-CHAIN_OUTBOUND/SPEC.md) |

### Extended

Upstream messages the fork adds fields to. Both are plain proto additions —
upstream clients ignore them, fork clients get more.

| Message | Field | Why | SPEC |
|---|---|---|---|
| `Connection` | `detourList` | the transport detour tail, which `chainList` omits by design | [017](../SPECS/TASKS/017-CONNECTION_DETOUR_CHAIN/SPEC.md) |
| `Group` | `mode` | `least_test` / `round_robin`; empty for non-urltest groups, so it doubles as "is this group balanced" without calling the gated `GetPool` | [019](../SPECS/TASKS/019-URLTEST_MODE_STICKY/SPEC.md) v2 |

### Changed

Same wire contract, different behaviour.

| What | Change |
|---|---|
| `Subscribe*.interval` | clamped to a 200 ms floor with a warning naming the unit; upstream honoured whatever it was given, which let a client spin the CPU (`fca1d367e`) |
| `Group.selected` | for `mode: round_robin` there is no single current node — the field carries the last node the balancer happened to pick. Treat it as a hint; read `GetPool` for real rotation state |

## Recipe: profiling a remote machine

A remote lxd machine exposes **only** this gRPC plane. Its Clash API is not
reachable and its `sing-box.log` is on a filesystem you do not have. Everything
below replaces both.

```
once at startup:
  GetRules            → rule table for resolving Connection.rule
  GetOutbounds        → tag table for resolving chains
  GetStartedAt        → uptime baseline

streams, held open:
  SubscribeConnections{interval: int64(time.Second)}   → the connection table
  SubscribeDNSQueries{includeAnswers: true}            → DNS + CNAME chains
  SubscribeStatus{interval: int64(time.Second)}        → totals, goroutines, memory

on demand:
  CloseConnection{id}                                  → kill one connection
  GetURLViaOutbound{outboundTag, link}                 → what does this node see
```

Client-side invariants, all of them earned the hard way:

1. Keep a table keyed by connection `id`. `NEW` inserts, `UPDATE` **adds deltas to
   the running totals** (the event has no connection object), `CLOSED` removes.
2. On `reset` — and on any stream error — clear the table.
3. Distinguish "no data yet" from "no connections". An empty table before the
   first frame is not an empty machine; rendering it as zero makes a graph drop to
   the floor on every reconnect. The launcher tracks this with a `live` flag in
   `ConnTracker`.
4. Send intervals as `int64(time.Second)`, not `1000`.

A working client of exactly this shape lives in the launcher:
`internal/traffic/grpc_tracker.go` (the table),
`core/services/lxd_remote_transport.go` (the remote stream and DNS decoding).

## Sources

- [`daemon/started_service.proto`](../daemon/started_service.proto) — the contract
- [`daemon/started_service.go`](../daemon/started_service.go) — delta construction:
  `SubscribeConnections`, `applyConnectionEvent`, `buildTrafficUpdates`
- [lxd-daemon.md](lxd-daemon.md) — transport, mTLS, admin REST
- [SPEC 065](../SPECS/TASKS/065-LXD_OBSERVABILITY_PLANE/SPEC.md) — profiling the
  daemon process itself (REST, not gRPC)
