# lxd gRPC API — observability for client tooling

> 🌐 Русская версия: **[lxd-grpc-api.ru.md](lxd-grpc-api.ru.md)** · Feature: [OBSERVABILITY](../SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md)

How a client (the launcher, a profiler, any diagnostic tool) reads live state out
of a `sing-box lxd` daemon over gRPC.

The proto is the contract; this document explains it. When the two disagree,
[`daemon/started_service.proto`](../daemon/started_service.proto) wins. This file
covers what the proto does not: delta semantics, which fields are populated in
which event, and the traps that have already caused production incidents.

For connecting to the daemon (mTLS, client certificates, admin REST) see
[lxd-daemon.md](lxd-daemon.md).

> ⚠️ The proto file lives in `daemon/`, not in `lxd/`. The daemon serves the
> same `StartedService` the Android line speaks. Anything added for mobile
> observability therefore reaches the server too, and vice versa.

## Table of contents

- [One contract, served over gRPC and libbox](#one-contract-served-over-grpc-and-libbox)
- [This document covers observability only](#this-document-covers-observability-only)
- [Fork RPCs exist only in builds with `with_lx_command`](#fork-rpcs-exist-only-in-builds-with-with_lx_command)
- [Rules that apply to every stream](#rules-that-apply-to-every-stream)
- [Connection plane](#connection-plane)
  - [`SubscribeConnections(SubscribeConnectionsRequest) → stream ConnectionEvents`](#subscribeconnectionssubscribeconnectionsrequest--stream-connectionevents)
  - [Populated fields depend on the event type](#populated-fields-depend-on-the-event-type)
  - [`Connection`](#connection)
  - [Closing connections](#closing-connections)
- [DNS plane](#dns-plane)
  - [`SubscribeDNSQueries(SubscribeDNSQueriesRequest) → stream DnsQueryEvent`](#subscribednsqueriessubscribednsqueriesrequest--stream-dnsqueryevent)
- [Supporting RPCs](#supporting-rpcs)
  - [Message field reference](#message-field-reference)
- [observability-api-lx](#observability-api-lx)
  - [Added](#added)
  - [Extended](#extended)
  - [Changed](#changed)
- [A remote machine is profiled over gRPC only](#a-remote-machine-is-profiled-over-grpc-only)
- [Sources](#sources)

---

## One contract, served over gRPC and libbox

This observability surface is not lxd-specific. The same set of RPCs and messages
is exposed over two carriers:

| Carrier | Where | Client entry point |
|---|---|---|
| **gRPC** (`StartedService`) | the `sing-box lxd` daemon — desktop / server | `daemon/started_service.proto` |
| **libbox `CommandClient`** | the Android / iOS AAR (gRPC over a local socket) | `experimental/libbox/command_client_command_lx.go` |

The lx extensions (`SubscribeDNSQueries`, `GetRules`, `GetGroups`, `GetOutbounds`,
`GetPool`, `GetDNSGroups`, `GetRunningConfig`, `GetEffectiveConfig`, `GetOutboundConfig`,
`URLTestOutbound`, `GetURLViaOutbound`, `GetChainStatus`, `GetWireGuardStatus`,
`GetTailscaleStatus`)
are implemented once in the core and reached through either carrier with the same field semantics. The field tables below
therefore describe the Android AAR just as much as a remote lxd machine. Only the
wire framing and the method-call syntax differ. Transport-specific rules are
called out where they apply. For example, without the tag libbox receives the same
`codes.Unimplemented`, but the app sees it as a plain error with text such as
`get rules: rpc error: code = Unimplemented desc = …`: there is no separate code,
and the failure can be recognised only by its text.

## This document covers observability only

`StartedService` is the data plane of the daemon: everything about the running
core (status, connections, DNS, groups, rules). `ManagedService` is the control
plane (stop, reload, system proxy). It is small enough to read straight from
[`daemon/managed_service.proto`](../daemon/managed_service.proto).

This document covers the observability subset of `StartedService`. The service
also carries dozens of Tailscale, OpenVPN, OpenConnect and USB-IP RPCs. None of
them relate to observing traffic, so they are left out here; read the proto.

REST endpoints for profiling the daemon process itself (memory, pprof, its own
log) are a separate plane on the admin port: `/admin/memory`, `/admin/stats`,
`/admin/logs`, `/admin/pprof/*`, plus host telemetry in `/admin/host`. They are
documented in [lxd-daemon.md](lxd-daemon.md).

## Fork RPCs exist only in builds with `with_lx_command`

Fourteen RPCs live inside an `lx:begin lx_command` … `lx:end lx_command` block and
exist only when the binary is built with `with_lx_command`:

`URLTestOutbound`, `GetRules`, `GetGroups`, `GetOutbounds`, `SubscribeDNSQueries`,
`GetPool`, `GetDNSGroups`, `GetRunningConfig`, `GetEffectiveConfig`, `GetOutboundConfig`,
`GetURLViaOutbound`, `GetChainStatus`, `GetWireGuardStatus`, `GetTailscaleStatus`.

The same block also carries control RPCs outside this document's scope:

- `SetChainPositionEnabled` — enabling and disabling positions of a `chain` at
  runtime ([CHAIN feature](../SPECS/FEATURES/015-CHAIN/FEATURE.md));
- `SetEndpointEnabled` — manual on/off of a WG/AWG or Tailscale endpoint, see
  [lx-energy.md §12](lx-energy.md#12-a-manual-switch-turns-an-endpoint-off-without-a-config-change);
- `TailscaleLogin` — login of a Tailscale endpoint with a new auth key without a box restart;
- `SetImpair`, `ScheduleImpair`, `GetImpairStatus` — runtime control of the `impair` test
  outbound, see [lx-impair.md](lx-impair.md#runtime-control).

Against a binary built without the tag they return `codes.Unimplemented`. The
generated client stubs still contain the methods: the gate is server-side, so
this is a runtime failure, not a compile error. A client that depends on the DNS
plane should probe once at startup (`GetRules` is the cheapest) and degrade
explicitly rather than surfacing `Unimplemented` per call.

Everything else in this document — `SubscribeConnections`, `SubscribeStatus`,
`CloseConnection`, `GetStartedAt` — is ungated and present in every build.

## Rules that apply to every stream

**`interval` is nanoseconds.** Every `Subscribe*Request.interval` is a
`time.Duration` count, following the sing-box/libbox convention. Values below the
200 ms floor are clamped and logged as a warning (`minSubscribeInterval` /
`clampSubscribeInterval` in [daemon/started_service.go](../daemon/started_service.go)).

> ⚠️ A client that sends `1000` meaning milliseconds gets 200 ms, not the 1 s it
> intended. Without the clamp the server read that value as 1 µs and the ticker
> burned a full CPU core: this happened with the launcher, and the clamp was added
> in `fca1d367e`. Send `int64(time.Second)`, never a bare number.

**Streams wait for the core.** Handlers call `waitForStarted` before doing
anything: subscribing while the core is starting blocks rather than failing. A
client may open its streams immediately after connecting.

**No core, no connection tracking.** If the instance has no `trafficManager`,
`SubscribeConnections` returns `Unimplemented`. Only the context distinguishes this
from the build-tag case. Do not treat `Unimplemented` as "wrong binary" without
checking whether the core is running at all.

**Stream break means your state is stale.** On a `Recv` error, every connection
you remember is unconfirmed, so drop the table. Holding it shows long-dead
connections as live.

## Connection plane

### `SubscribeConnections(SubscribeConnectionsRequest) → stream ConnectionEvents`

**This is a delta protocol, not a repeated snapshot.** Each `ConnectionEvents`
carries what changed, not the current list of connections. Treating it as the list
is the most common mistake.

```
ConnectionEvents { repeated ConnectionEvent events; bool reset; }
ConnectionEvent  { ConnectionEventType type; string id; Connection connection;
                   int64 uplinkDelta; int64 downlinkDelta; int64 closedAt; }
```

| `ConnectionEvent` field | Type | Meaning |
|---|---|---|
| `type` | enum | `CONNECTION_EVENT_NEW` (0) \| `CONNECTION_EVENT_UPDATE` (1) \| `CONNECTION_EVENT_CLOSED` (2) |
| `id` | string | connection UUID — the key you index by, present on every event |
| `connection` | `Connection` | full object on `NEW`, always `nil` on `UPDATE`, may be `nil` on `CLOSED` ([when](#populated-fields-depend-on-the-event-type)) |
| `uplinkDelta` / `downlinkDelta` | int64 | bytes since the last tick (not a rate), only on `UPDATE` |
| `closedAt` | int64 | Unix milliseconds, only on `CLOSED` |
| `ConnectionEvents.reset` | bool | this frame replaces your whole table |

**The first frame carries `reset = true`** and contains the full current state as
a batch of `NEW` events: every active connection, plus recently closed ones
(already carrying `closedAt`). `reset` also arrives mid-stream when the core
restarts under the client: an `apply`, a Start/Stop. On `reset`, clear your table
before applying the events in that same frame.

Subsequent frames arrive on two schedules. When the core opens or closes a
connection, a frame goes out immediately; these events are drained in a batch, so
one frame may hold many of them. Traffic deltas arrive on the interval tick.

### Populated fields depend on the event type

| Event type | `connection` | `uplinkDelta` / `downlinkDelta` | `closedAt` |
|---|---|---|---|
| `NEW` | full object | — | — |
| `UPDATE` | always `nil` | the delta since the last tick | — |
| `CLOSED` | usually full, may be `nil` | — | set |

**`UPDATE` never carries the connection object.** It holds only `id` and the two
deltas. A client that reads `ev.GetConnection()` and skips the event when it is
`nil` will show connections that never accumulate any traffic. The client must
maintain traffic counters itself, adding deltas onto the totals from the `NEW`
event.

**`CLOSED` may have a `nil` connection** when the metadata is no longer
retrievable. `id` and `closedAt` are always present, so key your removal on `id`,
never on the embedded object.

**Connections can close without a close event reaching you.** A connection may
disappear from the active set between ticks without producing a subscription
event. The server then synthesizes a `CLOSED` on the next tick. Its `closedAt` is
the current moment, not the real close time. `connection` is filled only if the
connection was still in the closed-connections ring. Treat `closedAt` as
approximate.

**A zero-delta `UPDATE` is meaningful.** It marks a connection that was
transferring and has stopped. On that edge a client drops the row's rate to zero
instead of leaving the last non-zero value on screen forever. Idle connections
that never moved produce no events at all.

**Counters can move backwards.** Connection IDs are reused. When the server sees a
negative delta it re-baselines and emits a zero `UPDATE` instead of a negative
one. A client will never receive a negative delta, but should not assume totals
are monotonic across an ID it has seen before.

### `Connection`

| Field | Type | Meaning |
|---|---|---|
| `id` | string | UUID; the key for `CloseConnection` and for your own table |
| `inbound`, `inboundType` | string | which inbound accepted it |
| `network` | string | `tcp` / `udp` |
| `ipVersion` | int32 | 4 or 6 |
| `source`, `destination` | string | `host:port`; destination may be an IP or a hostname |
| `domain` | string | the sniffed domain; empty when sniffing did not fire |
| `protocol` | string | sniffed application protocol (`http`, `tls`, `quic`, …) |
| `user` | string | inbound auth user, when the inbound has auth |
| `createdAt`, `closedAt` | int64 | Unix milliseconds; `closedAt` zero while open |
| `uplink`, `downlink` | int64 | current rate (bytes/s) |
| `uplinkTotal`, `downlinkTotal` | int64 | bytes accumulated by this connection |
| `rule` | string | the matched rule, rendered as a string; `GetRules` gives the structure |
| `outbound`, `outboundType` | string | the final outbound |
| `chainList` | repeated string | the outbound chain, final → outward |
| `detourList` | repeated string | **lx**: the final outbound's transport detour tail |
| `processInfo` | `ProcessInfo` | `processId` (uint32), `userId` (int32), `userName`, `processPath`, `packageNames` (repeated) |
| `fromOutbound` | string | set when the connection originated from an outbound itself |

**`chainList` omits the detour by design**, hence `detourList`, which the fork
adds. Order is final outbound → outward. Empty for outbounds without a detour. A
profiler that shows the full path needs both lists, concatenated.

**`domain` versus `destination`.** A client that wants to know the host should
prefer `domain` and fall back to the host part of `destination`. The launcher does
this in `ProtoConnToClash` (`singbox-launcher/internal/traffic/grpc_tracker.go`).

### Closing connections

- `CloseConnection(CloseConnectionRequest{id})` — one connection, by UUID.
- `CloseAllConnections(Empty)` — all of them.

## DNS plane

### `SubscribeDNSQueries(SubscribeDNSQueriesRequest) → stream DnsQueryEvent`

**A remote client does not need the core's text log for DNS.** This stream is the
structured equivalent, and it carries attribution the log does not have. On a
remote machine the log file is on someone else's filesystem and Clash API
`/connections` is not exposed.

The request has a single field: `includeAnswers` (bool). Set it `true` to receive
the response records in `answers`.

| Field | Type | Meaning |
|---|---|---|
| `domain` | string | the queried name |
| `queryType` | uint32 | DNS type code of the question (1 = A, 28 = AAAA, 65 = HTTPS, …) |
| `rcode` | int32 | response code; `-1` when there was no response at all |
| `ttl` | uint32 | answer TTL, seconds |
| `source` | string | resolver verb: `exchanged` / `cached` / `optimistic` / `refreshed` / `failed` |
| `failed` | bool | true on timeout / SERVFAIL / rejected — failures are first-class |
| `error` | string | the failure detail, when `failed` |
| `answers` | repeated `DnsAnswer` | `{name, type (uint32), rdata, ttl (uint32)}` in wire order; only when `includeAnswers` |
| `dnsServer` | string | which transport (DNS server tag) resolved it |
| `dnsServerType` | string | that server's type |
| `outbound` | repeated string | the channel(s) that server is bound to; empty on cached/optimistic |
| `processInfo` | `ProcessInfo` | app attribution — package / uid |
| `dnsGroupPath` | repeated string | group nesting, inside-out; empty = no group involved |
| `attempts` | repeated `DnsGroupAttempt` | probe chronology at answer time: `{server, serverType, outcome, rttMs (uint32)}` |
| `fanned` | bool | a fan-out (rescue / election / parallel) was involved |
| `survival` | bool | answer came from the least-dirty server when none was clean |

`DnsGroupAttempt.outcome` vocabulary: `answered` \| `timeout` \| `network_error` \|
`servfail`. `answered` includes NXDOMAIN and empty answers: they are valid
responses, not failures.

**CNAME chains come from `answers`.** The response records arrive in wire order:
CNAME hops first, then the A/AAAA records. Walk them in order to reconstruct the
chain. There is no separate CNAME field and none is needed. The launcher does this
in `core/services/lxd_remote_transport.go` (see `dnsTypeCNAME`).

**Empty `outbound` is state, not a bug.** It means the query never left the
device: the answer was cached or optimistic. Likewise, an empty `dnsGroupPath`
means the query did not go through a DNS group. Check the SPEC semantics before
suspecting the core.

**`attempts` is a snapshot, not the whole truth.** It deliberately omits fan-out
stragglers that resolved after the answer was returned. `GetDNSGroups` gives the
full picture.

## Supporting RPCs

| RPC | Use |
|---|---|
| `SubscribeStatus(interval) → stream Status` | memory, goroutines, `connectionsIn`/`connectionsOut`, rate and totals — the header line of a profiler |
| `GetStartedAt → StartedAt` | core start time, for uptime |
| `GetRules → RuleList` | **lx**: structured rules — resolves `Connection.rule` |
| `GetGroups → Groups` / `SubscribeGroups` | **lx** (`GetGroups`) / upstream (stream) — group state |
| `GetOutbounds → OutboundList` | **lx**: outbound tags, to resolve chain entries; WG/AWG state (`endpointState`); Tailscale `up`/`disabled`/`down` |
| `GetWireGuardStatus(tag) → WireGuardEndpointStatus` | **lx**: one WG/AWG endpoint — device state and per-peer last handshake, address, transfer |
| `GetTailscaleStatus(endpointTag) → TailscaleEndpointStatus` | **lx**: one Tailscale endpoint, a fresh snapshot: per-peer path (direct / peer relay / DERP), handshake, backend health |
| `SubscribeTaildropInbox(endpointTag) → stream TaildropInbox` | upstream; **lx**: a received file carries `senderID`, the sender's stable node ID, empty for files that were on disk before the core restarted |
| `GetPool(GetPoolRequest) → PoolList` | **lx**: rotation state of a balanced urltest group |
| `GetDNSGroups → DnsGroupList` | **lx**: full DNS group state |
| `GetRunningConfig → RunningConfig` | **lx**: the config the core is running |
| `GetEffectiveConfig → RunningConfig` | **lx**: the same config after the lx pre-pass — chain hops, rewritten `detour`, aligned MTU, `lx.detour` expanded |
| `GetOutboundConfig(tag) → OutboundConfig` | **lx**: one node as JSON `{tag, original, effective, changes}`; an unknown tag is `NotFound` |
| `URLTestOutbound → delay` | **lx**: probe a single node |
| `GetURLViaOutbound → body` | **lx**: probe returning the response body — which exit IP this node gives |
| `GetChainStatus(tag) → ChainState` | **lx**: state of one `chain` outbound — per position the resolved node, `transparent`, `disabled`, errors; dial and error counters |
| `SubscribeLog → stream Log` | the core's log, not the daemon's; the daemon's log is served by the admin REST ([lxd-daemon.md](lxd-daemon.md)) |

`Status.trafficAvailable` distinguishes "zero traffic" from "traffic accounting
not available". When it is false, do not render zeros.

### Message field reference

The response shapes of the supporting RPCs.

**`Status`** (`SubscribeStatus`) — the profiler header line:

| Field | Type | Meaning |
|---|---|---|
| `memory` | uint64 | resident bytes of the core process |
| `goroutines` | int32 | live goroutine count |
| `connectionsIn` / `connectionsOut` | int32 | active inbound / outbound connections |
| `trafficAvailable` | bool | false = accounting off; do not render the byte fields as zero |
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
| `Group.selected` | string | current node; for `round_robin` only a hint, read rotation from `GetPool` |
| `Group.isExpand` | bool | UI expand state |
| `Group.mode` | string | **lx**: `least_test` / `round_robin`; empty for non-urltest groups |
| `Group.items` | repeated `GroupItem` | member nodes |
| `GroupItem.tag`, `GroupItem.type` | string | node tag and type |
| `GroupItem.urlTestTime` | int64 | Unix ms of the last probe |
| `GroupItem.urlTestDelay` | int32 | last probe delay in ms; `0` = dead / not measured |

**`PoolSlot`** (`GetPool` → `PoolList.slots`) — **lx**:

| Field | Type | Meaning |
|---|---|---|
| `slot` | uint32 | fixed slot index in the rotation |
| `tag` | string | the node currently in that slot |
| `delay` | uint32 | last test result in ms; `0` = dead / not measured (the server clamps a live node to ≥ 1) |

A non-`round_robin` group returns an empty `slots` list: "no pool here", not an
error.

**`DnsGroupState`** / **`DnsGroupMember`** (`GetDNSGroups`) — **lx**:

| Field | Type | Meaning |
|---|---|---|
| `DnsGroupState.tag`, `.mode`, `.current` | string | group tag, mode, sticky target |
| `DnsGroupMember.tag`, `.serverType` | string | member tag and server type |
| `DnsGroupMember.clean` | bool | zero live errors |
| `DnsGroupMember.liveErrors` | uint32 | current live error count |
| `DnsGroupMember.lastErrorAgeMs` | int64 | age of the newest live error; `-1` = none |
| `DnsGroupMember.liveWins` | uint32 | live win records (fastest mode) |
| `DnsGroupMember.current` | bool | is this the group's sticky target |
| `DnsGroupMember.lastRttMs` | uint32 | last successful probe; `0` = never measured |

**`URLTestOutbound`** (**lx**) — request `{outboundTag, link, timeout}`, response
`{delay (uint32, ms), error}`. `timeout` is in milliseconds; `0` = no extra
deadline (bounded only by the call). On failure `delay` is unset and `error` is
populated.

**`GetURLViaOutbound`** (**lx**) — a diagnostic HTTP probe through one node. It
returns the response body and answers which exit IP, geo or warp state this node
gives:

| Request field | Type | Meaning |
|---|---|---|
| `outboundTag` | string | the node to probe through |
| `link` | string | URL to fetch |
| `timeout` | uint32 | ms; `0` = no extra deadline |
| `maxBytes` | uint32 | body cap; `0` → 256 KiB default, hard ceiling 1 MiB |
| `headers` | repeated `{key, value}` | extra request headers |

| Response field | Type | Meaning |
|---|---|---|
| `httpStatus` | uint32 | HTTP status code |
| `body` | bytes | response body; bytes rather than string, because an arbitrary endpoint is not guaranteed to return valid UTF-8 |
| `truncated` | bool | body hit `maxBytes` and was cut |
| `contentType` | string | response `Content-Type` |
| `remoteAddr` | string | the exit address the request left from |
| `elapsedMs` | uint32 | round-trip time |
| `error` | string | set when the fetch never completed (unknown tag, dial/TLS failure, timeout) |

**`RunningConfig`** (`GetRunningConfig`, **lx**): a single `content` field
(string) — the canonical serialization of the options the core built from
(post-override). It is not byte-identical to the profile text the client sent:
field order, `omitempty` and `[]`→`null` differ. Compare semantically, not
textually. `GetEffectiveConfig` returns the same message with the config after
the lx pre-pass, in the same canonical form.

**`OutboundConfig`** (`GetOutboundConfig`, **lx**): a single `content` field
(string), a JSON object `{tag, original, effective, changes}`. `original` is the
node as in `GetRunningConfig`, `effective` as in `GetEffectiveConfig`; a chain hop
has only `effective`. `changes` is a list of `{field, from, to, by}` in the order
they were made, and applied to `original` it gives `effective`; the fields are in
[lx-observability.md](lx-observability.md).

## observability-api-lx

What the fork does to this plane relative to upstream sing-box.

### Added

Fourteen RPCs behind `with_lx_command`, all absent upstream:

| RPC | Why |
|---|---|
| `SubscribeDNSQueries` | structured DNS stream; failures first-class; replaces log parsing |
| `GetRules` | rule table, to resolve `Connection.rule` |
| `GetGroups`, `GetOutbounds` | one-shot reads where upstream offers only streams |
| `GetWireGuardStatus`, `GetTailscaleStatus` | per-endpoint status on demand; `GetOutbounds` stays a plain node list |
| `GetPool` | rotation state of a balanced urltest group |
| `GetDNSGroups` | DNS group state |
| `GetRunningConfig` | the config in effect |
| `GetEffectiveConfig`, `GetOutboundConfig` | the config as built after the lx pre-pass, and what changed in each node |
| `URLTestOutbound` | probe one node, get a delay |
| `GetURLViaOutbound` | probe one node, get the response body |
| `GetChainStatus` | state of one `chain` outbound by tag |

### Extended

Upstream messages the fork adds fields to. Both fields are plain proto additions:
upstream clients ignore them, fork clients get more.

| Message | Field | Why |
|---|---|---|
| `Connection` | `detourList` | the transport detour tail, which `chainList` omits by design |
| `Group` | `mode` | `least_test` / `round_robin`; empty for non-urltest groups, so it also tells whether the group is balanced without calling the gated `GetPool` |

### Changed

Same wire contract, different behaviour.

| What | Change |
|---|---|
| `Subscribe*.interval` | clamped to a 200 ms floor with a warning naming the unit. Upstream honoured whatever it was given, which let a client spin the CPU (`fca1d367e`) |
| `Group.selected` | for `mode: round_robin` there is no single current node. The field carries the last node the balancer happened to pick; treat it as a hint and read rotation state from `GetPool` |

## A remote machine is profiled over gRPC only

A remote lxd machine exposes only this gRPC plane. Its Clash API is not reachable
and its `sing-box.log` is on a filesystem you do not have access to. The calls
below replace both.

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

Four client-side invariants; breaking each of them has already caused bugs:

1. Keep a table keyed by connection `id`. `NEW` inserts, `UPDATE` adds deltas to
   the running totals (the event has no connection object), `CLOSED` removes.
2. On `reset` and on any stream error, clear the table.
3. Distinguish "no data yet" from "no connections". An empty table before the
   first frame does not mean an empty machine. Rendering it as zero makes a graph
   drop to zero on every reconnect. The launcher tracks this with a `live` flag in
   `ConnTracker`.
4. Send intervals as `int64(time.Second)`, not `1000`.

A working client of this shape lives in the launcher:
`internal/traffic/grpc_tracker.go` (the table),
`core/services/lxd_remote_transport.go` (the remote stream and DNS decoding).

## Sources

- [`daemon/started_service.proto`](../daemon/started_service.proto) — the contract
- [`daemon/started_service.go`](../daemon/started_service.go) — delta construction:
  `SubscribeConnections`, `applyConnectionEvent`, `buildTrafficUpdates`
- [lxd-daemon.md](lxd-daemon.md) — transport, mTLS, admin REST
- [SPEC 065](../SPECS/TASKS/065-LXD_OBSERVABILITY_PLANE/SPEC.md) — profiling the
  daemon process itself (REST, not gRPC)
