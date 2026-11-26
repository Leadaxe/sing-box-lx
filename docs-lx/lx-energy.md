# sing-box-lx — energy optimization on mobile (idle-suspend × urltest)

> 🌐 Русская версия: **[lx-energy.ru.md](lx-energy.ru.md)**.
>
> Features: [ENERGY](../SPECS/FEATURES/008-ENERGY/FEATURE.md) (idle-suspend), [URLTEST_BALANCE](../SPECS/FEATURES/007-URLTEST_BALANCE/FEATURE.md) (round_robin/pool/failover), [AWG](../SPECS/FEATURES/003-AWG/FEATURE.md). Config keys of all lx features: [lx-config.md](lx-config.md).

This document explains why the fork saves battery on Android and how to control it. Upstream sing-box keeps every WireGuard/AmneziaWG endpoint alive 24/7 regardless of traffic. Its recv-workers hold buffers: ~8 MB per worker at the mobile `BatchSize=128`. These buffers are the dominant source of GC heat: on a device, 8 suspended endpoints freed 134 MB. Keepalive and handshake timers wake the radio. The fork selectively suspends idle endpoints and bounds health-check probes so they do not wake sleepers.

## Table of contents

- [1. The savings come from three layers: reachability, tick, endpoint](#1-the-savings-come-from-three-layers-reachability-tick-endpoint)
- [2. An endpoint is reachable if a new connection can land on it](#2-an-endpoint-is-reachable-if-a-new-connection-can-land-on-it)
- [3. Unreachable and reachable nodes fall asleep on different thresholds](#3-unreachable-and-reachable-nodes-fall-asleep-on-different-thresholds)
- [4. The tick checks six conditions before suspending an endpoint](#4-the-tick-checks-six-conditions-before-suspending-an-endpoint)
- [5. A wake after Down costs +1 RTT, after Close a rebuild](#5-a-wake-after-down-costs-1-rtt-after-close-a-rebuild)
- [6. urltest probes wake sleepers, so five mechanisms bound them](#6-urltest-probes-wake-sleepers-so-five-mechanisms-bound-them)
- [7. Timelines](#7-timelines)
  - [Night (round_robin pool=3, interval=15m, idle_timeout=30m, thresholds 30s/30m)](#night-round_robin-pool3-interval15m-idle_timeout30m-thresholds-30s30m)
  - [Selector switching away from a group](#selector-switching-away-from-a-group)
- [8. Recommended mobile configuration](#8-recommended-mobile-configuration)
- [9. Sleep cuts no connections and keeps the bookkeeping intact](#9-sleep-cuts-no-connections-and-keeps-the-bookkeeping-intact)
- [10. Suspend, wake and teardown show up in the log](#10-suspend-wake-and-teardown-show-up-in-the-log)
- [11. Lazy build and the build budget keep devices from being built for nothing](#11-lazy-build-and-the-build-budget-keep-devices-from-being-built-for-nothing)
- [12. A manual switch turns an endpoint off without a config change](#12-a-manual-switch-turns-an-endpoint-off-without-a-config-change)

---

## 1. The savings come from three layers: reachability, tick, endpoint

```
┌─────────────────────────────────────────────────────────────────┐
│ LAYER 1 · REACHABILITY (Router)                                 │
│ "Where can traffic land right now?"                             │
│ event-driven cache: recomputed only when the active tree changes│
└───────────────┬─────────────────────────────────────────────────┘
                │ reachable[tag] → bool
┌───────────────▼─────────────────────────────────────────────────┐
│ LAYER 2 · THE TICK (Router, every max(threshold/2, 5s),         │
│ pause-aware) — for every WG/AWG endpoint:                       │
│ SuspendIfIdle(reachable, T1, T2)                                │
└───────────────┬─────────────────────────────────────────────────┘
                │ gate-chain decision (§4)
┌───────────────▼─────────────────────────────────────────────────┐
│ LAYER 3 · THE ENDPOINT (Down/Up + Close/Rebuild)                │
│ Down: workers exit, buffers freed, timers silent                │
│ Close (slept past lx.wg.idle.teardown): the netstack goes too   │
│ Wake is dial-only: +1 RTT / rebuild ~0.5-1 s                    │
└─────────────────────────────────────────────────────────────────┘
```

The urltest health check (probes) is not part of these layers. Every probe is an ordinary dial, so it wakes sleeping endpoints. Half of the optimizations limit probes to the cases where they are needed (§6).

## 2. An endpoint is reachable if a new connection can land on it

An endpoint is reachable if a new connection can land on it right now. The walk starts from three entry points (seeds):

- `route.final` (the default outbound);
- the target of every route/bypass rule;
- every DNS server's detour. DNS is dialed on every resolution, so a DNS-only node is always reachable. Otherwise it would flap Down/Up around every quiet gap and add a handshake to the first DNS query.

The walk descends as follows, transitively, with cycles cut:

- a selector → its current choice only (`Now()`);
- a round_robin urltest → its whole current pool;
- a legacy urltest → the currently selected node;
- an ordinary outbound → its detour chain.

The set is cached and recomputed only on events: a selector switch, a urltest auto-switch (including the very first selection), a pool rebuild, a config reload. Between events the tick reads the ready map: one map lookup plus a couple of atomics per endpoint.

## 3. Unreachable and reachable nodes fall asleep on different thresholds

```jsonc
"lx": {
  "wg": {
    "idle": {
      "suspend": "30s",                // threshold for UNREACHABLE endpoints (feature switch)
      "suspend_reachable": "5m",       // optional: threshold for REACHABLE endpoints
      "teardown": "5m"                 // optional: how long to SLEEP before full teardown;
                                       //   defaults to suspend_reachable, "0" = off
    }
  }
}
```

The keys live in the `lx.wg.idle` block of the root `lx` block ([lx-block.md](lx-block.md)).
The flat `lx.wg.idle_suspend` and the rest of the old layout fail to load with an error naming the new key.
The keys `route.lx_idle_suspend` / `route.lx_idle_suspend_reachable` / `route.lx_idle_teardown`
were removed on 2026-11-02: a config with them fails to load as one with unknown keys.

| | Unreachable endpoint | Reachable endpoint | Any sleeping endpoint |
|---|---|---|---|
| Threshold | `lx.wg.idle.suspend` (30s idle) | `lx.wg.idle.suspend_reachable` (5m idle); `0`/absent — never | `lx.wg.idle.teardown` (5m of sleep, counted from falling asleep); an explicit `0` never tears down, an absent key inherits the reachable window |
| What happens | `Down()` — freeze | `Down()` — freeze | `Close()` — full teardown: the gVisor netstack (~6 MB/node) goes too |
| Wake | dial, +1 RTT | dial, +1 RTT | dial, rebuild ~0.5–1 s on the first request |

The third level is deep sleep. A freeze (`Down`) releases recv-buffers and timers, but the netstack stays. A node that has slept through one more window is torn down entirely; nothing but its config remains. The sleep window counts from the moment of falling asleep, not from the last dial, so it does not depend on which threshold put the node to sleep. Teardown leaves live connections alone: a node with flows never falls asleep (§4), and only sleepers are torn down.

The core checks that the reachable threshold is set together with `lx.wg.idle.suspend` and is not below it. Recommendation: `>=` the `idle_timeout` of your urltest groups; §7 explains why.

Idle time is the time since the last dial through the endpoint, i.e. since a new connection was created. Data on established connections bypasses the dial path; gates §4.5–4.6 protect it.

## 4. The tick checks six conditions before suspending an endpoint

Every tick, each endpoint goes through a chain of checks, cheapest first:

1. **Listen mode** (`listen_port` set) → never suspend: inbound peers have no way to wake it.
2. **Window selection**: unreachable → `lx.wg.idle.suspend`; reachable → `lx.wg.idle.suspend_reachable` (or bail out if unset).
3. **Idle clock**: `IdleSince() < window` → too early.
4. **Already down** (stopped or closed) → someone else's state, don't touch.
5. **Live TCP flows**: the device gVisor stack's `CurrentEstablished` gauge > 0 → established connections exist (a download, a push socket) → don't suspend. The gate is precise, and keepalives do not affect it: a keepalive is not a TCP flow.
6. **Counter delta**: rx+tx grew by ≥ 4096 bytes since the previous decision → live UDP/QUIC traffic → refresh the clock, don't suspend. The gate compares against a threshold instead of catching any change: WG keepalive (32 B/interval) and rekeys (~240 B/~2 min) move the counters forever. A `persistent_keepalive` peer must still be able to sleep, because silencing its timers is half the win. A silent QUIC flow with rare pings below the threshold will be suspended. That is an accepted trade-off: QUIC migrates or re-establishes.
7. All quiet → `device.Down()`, log `lx idle: suspend <tag>`.

## 5. A wake after Down costs +1 RTT, after Close a rebuild

`Down()` (levels 1–2) is a freeze, not a Close. The UDP socket closes: recv-workers exit and release their buffers, which is the main RAM and GC win. Session keys are zeroed, and all peer timers stop: keepalive, retransmit, the AWG junk machinery. Objects and the port survive the cycle.

`Close()` (level 3, `lx.wg.idle.teardown`) is a teardown: the netstack, the Device/Peer objects, the queues and every device goroutine are gone; only the config remains in memory. Waking rebuilds from scratch: ~0.5–1 s on the first dial instead of +1 RTT.

What each level buys, measured on a CPH2411 device:

| Level | Frees | Measured |
|---|---|---|
| 1–2 (`Down`) | recv-workers + their buffers (~8 MB/worker, 2 per device), timers | −134 MB with 8 endpoints suspended (`bufsArrs` 223.93→89.89 MB) |
| 3 (`Close`) | netstack + Device objects + every device goroutine | ~5.9 MB/node; with 3 nodes the heap barely moved (36.7→36.3 MB) |

The level-3 delta is small for a reason outside the feature: 63% of the heap (23.4 MB) is held by the global `sing/common/buf` pool (`GetOutboundBuffer → buf.Get`). The pool is process-wide, deliberately survives both `Down` and `Close`, and never returns memory to the OS. Against it, three netstacks are noise.

Level 1 delivers most of the RAM savings. Level 3 pays off with dozens of nodes, not three: there it reclaims another ~6 MB per sleeper. On a small config it saves nothing noticeable. Its second benefit is fewer live objects under the `SetMemoryLimit` ceiling, i.e. less GC for the nodes in use.

Only a dial through the endpoint wakes it (`DialContext`/`ListenPacket`/L3 forwarding). The dial re-opens the socket and workers, and the first packet waits for a fresh handshake: +1 RTT (14–21 ms on a nearby server, up to ~450 ms intercontinental). Throughput is unaffected.

The +1 RTT cannot be removed. Waking with the crypto session intact (`BindUpdate` leaves the keys alone) was rejected for two reasons. A WireGuard session lives 120 s and is invalid past 180 s, while sleep lasts minutes, so the handshake happens anyway. On mobile, a network change (Wi-Fi↔LTE) during sleep makes the endpoint the server knows stale, and on CGNAT paths the "saving" turns into a timeout instead of a normal handshake. Full reasoning: [SPEC 020](../SPECS/TASKS/020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md), section "ОТВЕРГНУТО: путь A".

Two frequent questions about waking:
- **The attempt itself wakes, not its success.** The wake happens at dial entry, before anything is sent. If the connection then fails (target unreachable), the tunnel is already awake and will fall asleep again after its window. Any app whose traffic is routed into this tunnel wakes it; traffic going direct or through another channel does not.
- **An open TCP socket keeps the tunnel awake** (the established-flows gate, §4.5). So for TCP there is no "app writes into a socket while the tunnel already sleeps" case. A silent UDP/QUIC session may fall asleep; QUIC then re-establishes with a fresh dial, and that dial wakes the tunnel.

Two events do not wake an endpoint: screen-on and a network change (pause-wake). The transport remembers a `suspended` flag and skips such devices. Otherwise one screen cycle would wake every device and desync the bookkeeping until restart.

For domain destinations, DNS resolution happens before the wake, so a failed lookup does not pay a handshake. A sleeping tunnel does not break DNS. Ordinary DNS does not go through the tunnel: a failed lookup means DNS is broken even with the tunnel awake. A DNS server detoured through this very tunnel sends its query as a separate dial to the server's IP, and that dial wakes the tunnel itself. The "asleep, therefore can't resolve" deadlock cannot happen.

## 6. urltest probes wake sleepers, so five mechanisms bound them

A health-check probe is an ordinary dial through the node, so it wakes a sleeping node. Five mechanisms bound probes:

| Mechanism | Level | Effect |
|---|---|---|
| `idle_timeout` (upstream, 30m) | group | no traffic through the group for longer → the probe ticker stops for good; the next dial (`Touch`) restarts it and runs an immediate re-test |
| Reachability gate (lx) | group | group unreachable (selector left) → cycles are skipped immediately, without waiting for idle_timeout; the ticker stays alive and self-recovers |
| `mode: failover` (lx) | mode | only the held node is probed each `interval` (one probe instead of N); other nodes are probed only when the held one fails (full run) or on a manual test |
| round_robin pool (lx) | mode | only pool members are probed (e.g. 3), not all N nodes; out-of-pool nodes only when refilling holes |
| Manual test | — | always full (force), never gated |

An active least_test group probes every member each `interval` and wakes sleepers. These are upstream semantics: picking the best node requires measuring all of them. Where that cost matters, pick `mode: failover`: the group holds the working node and probes only it, so the other nodes sleep for as long as the held one answers.

`failover` has its own price: while the held node works, the group does not look for a faster one, and the delays of the other nodes in the UI stay as of the last full run. A held node that fails a probe or a "path is dead" dial moves the selection to the fastest live node; a manual test measures everyone and re-selects the fastest. Mode semantics: [lx-urltest.md](lx-urltest.md#4-failover--hold-until-it-fails).

## 7. Timelines

### Night (round_robin pool=3, interval=15m, idle_timeout=30m, thresholds 30s/30m)

```
T=0        last user dial
T=0..30m   probe tail: the ticker is still alive, probes at T=15m and T=30m
           dial the pool (nodes don't sleep — every probe resets their clocks)
T≈45m      Since(lastActive) > idle_timeout → probe ticker STOPS
T≈60m      pool members' idle (since the last probe at T=30m) > 30m →
           the tick suspends all 3 nodes — complete silence until morning
MORNING    the first dial wakes ONE node (+1 RTT); Touch restarts the ticker;
           an immediate re-test refreshes the pool within seconds
```

A probe cannot wake a node that is falling asleep: probes go silent at `T = idle_timeout`, and reachable nodes fall asleep no earlier than `T = last probe + reachable`. With `reachable >= idle_timeout` the probes have stopped by the time the nodes sleep. Violating the recommendation breaks nothing: the tail gets 1–2 flap cycles.

### Selector switching away from a group

```
T=0   the selector moves off group AUTO
      → InvalidateReachability: AUTO and its members become unreachable
T=0+  AUTO's scheduled probe cycles are SKIPPED (reachability gate)
~30s+ AUTO's members fall asleep on the SHORT threshold (they are unreachable)
T=30m idle_timeout retires AUTO's ticker for good
```

Without the reachability gate an abandoned group would keep probing and waking all members for another 30 minutes: up to 10 cycles × N handshakes for nothing.

## 8. Recommended mobile configuration

```jsonc
{
  "lx": {
    "wg": {
      "idle": {
        "suspend": "30s",
        "suspend_reachable": "30m"
      }
    }
  },
  "outbounds": [{
    "type": "urltest",
    "tag": "auto",
    "outbounds": ["node-1", "…", "node-N"],
    "interval": "15m",          // rarer cycles where they still run
    "idle_timeout": "30m",      // = the reachable threshold (see §7)
    "mode": "failover"          // hold the working node, probe only it (one wake per interval)
    // load balancing instead: "mode": "round_robin", "balancer": { "pool": 3, "pool_tolerance": 0 }
  }]
}
```

Threshold coordination rules:

| Constraint | Enforced by | Consequence of violation |
|---|---|---|
| `interval <= idle_timeout` | core (upstream), start error | — |
| `lx.wg.idle.suspend_reachable >= lx.wg.idle.suspend` | core (lx), start error | — |
| `lx.wg.idle.suspend_reachable >= idle_timeout` of groups | recommendation | 1–2 probe flaps in the falling-asleep tail |
| a low reachable value (e.g. `5m`) | allowed | fast sleep during pauses; the cost is the same 1–2 extra handshakes during the first ~half hour of silence (until probes retire) plus +1 RTT on the first connection after every ≥5m pause |
| `interval` > MASQUE idle (5m) for groups with MASQUE nodes | recommendation | probes keep the MASQUE tunnel from sleeping |
| `pool_tolerance <= 15000` | core (lx), start error | — |

Notes:
- `pool_tolerance > 0` is discouraged on mobile: in that mode the group measures all candidates every cycle and wakes every sleeper outside the pool.
- `persistent_keepalive` on peers is compatible with suspend: the timer is stopped while asleep, and keepalive noise does not block falling asleep (§4.6). An awake node with keepalive keeps waking the radio; that is the user's choice.
- Without `lx.wg.idle.suspend` the whole feature is off (kill switch, zero overhead). It exists only in the mobile AAR (`with_lx_idle_suspend`); a desktop binary given this key fails fast at start with an explicit error.

## 9. Sleep cuts no connections and keeps the bookkeeping intact

- **Live connections are never cut**: a node carrying an established TCP flow or bulk UDP traffic is not suspended, however "idle" it looks by dials (§4.5–4.6). The `interrupt_exist_connections=false` promise holds.
- **Screen-off/on and network changes don't desync anything**: suspended devices stay suspended through pause/wake cycles.
- **The first request after sleep always works**: it goes through the last known node and wakes it, while an immediate re-test refreshes the selection in parallel. If that node died overnight, one request fails and the re-test repairs the pool right away.
- **Kill switch**: without `lx.wg.idle.suspend` not even the tick starts.

## 10. Suspend, wake and teardown show up in the log

- `lx idle: suspend <tag> idle=…` / `lx idle: wake <tag> by=dial` — edge-triggered pairs in the log. Frequent suspend↔wake pairs for one tag mean flapping: check threshold coordination (§8) and that the probe gates are active.
- A sleeping node has 0 recv-workers for its device (goroutine profile) and zero rx/tx.
- A node won't sleep although it should: check whether it is reachable (selector, pool, rule, DNS detour — §2) and whether it carries established TCP or bulk UDP.
- Probes run at night: check whether there is traffic through the group (`Touch` keeps the ticker alive). Background push connections count as traffic too.
- `lx idle: teardown <tag> by=budget` — the build budget tore the node down to make room for another (§11); `lx idle: build over budget N+1/N <tag>` — a build went over `build_max` because every built node carried live connections (`build_overflow: "build"`); `lx idle: build <tag>: build budget exhausted (N built, none idle)` — the dial gave up waiting for a slot (`build_overflow: "wait"`).

## 11. Lazy build and the build budget keep devices from being built for nothing

Suspend and teardown free an endpoint's memory once it has been idle. At start, though, a config with a dozen AWG nodes pays for all of them at once. Each device allocates its receive batches the moment it starts: 128 × 64 KiB per receive routine on Linux and Android, where the 64 KiB segments feed GRO. At the default that is 16.4 MiB per node (≈17.5 MB measured on lx.8, before SPEC 158). `lx.wg.buffers.read_datagram_count` lowers it in any build ([lx-block.md](lx-block.md)). Two keys keep devices from being built for nothing.

**`lx.wg.idle.lazy_build: true`** — endpoints start at level 3 (§5): the detour is resolved at start, as without the key, but the device and its netstack are not built. The first dial through the node (a user connection or a urltest probe) builds it along the same path a wake after teardown takes, and pays that cost once: the build plus the first handshake. A listen-mode endpoint (`listen_port`) is never lazy — nothing dials it. Requires `lx.wg.idle.suspend`.

**`lx.wg.idle.build_max: N`** — at most N devices are built at once. Without a cap, laziness only postpones the peak: a urltest probe of the whole group builds every node right after start. With a cap, a dial that needs device N+1 first tears down a victim:

1. devices built over the cap (`build_overflow: "build"`) go first;
2. then the node with the fewest **manual** refs — edges from a selector's current choice, the `final` outbound, a rule target;
3. then the fewest **auto** refs — a urltest pool, another node's `detour`, a DNS server's `detour`, a chain position;
4. then the node dialed longest ago.

The order is lexicographic: any number of auto refs loses to one manual ref. A ref is an edge leading straight into the node; nothing is inherited along the tree (selector → urltest → X gives X one auto ref). Sleep plays no part in the order: an asleep node with a ref outlives an awake node without one. Traffic plays no part in the order either; it only guards: a node with a dial in flight, an established TCP flow or ≥ 4096 bytes moved since the budget's previous sample of it is never torn down. A node without refs that was built a moment ago is a legal victim — probes cannot wait out a sleep threshold.

When every built node is guarded, `lx.wg.idle.build_overflow` decides:

| Value | Behaviour |
|---|---|
| `wait` (default) | the dial waits for a slot until its own deadline, at most 15 s; a released slot, a node falling asleep or a flow ending (checked every second) lets it retry. Past the deadline the dial fails and the node counts as unreachable for this probe, not dead |
| `build` | the device is built over the cap with a warning; the next build tears the over-cap device down first |

Probes under a cap run in waves: a group of K nodes with `build_max: N` is measured N builds at a time, so a probe round takes longer while memory stays bounded. If a wave does not fit into the probe timeout, the tail of the group is reported unavailable for that round.

`build_max` without `lazy_build` is legal: devices are built at start, and the cap applies from the first rebuild (a wake after `idle.teardown`). A cap below the number of nodes in simultaneous use makes nodes tear each other down on every switch (each dial pays a build); LxBox sets `build_max` for probe sessions only.

**States.** `GetOutbounds` reports each WG/AWG endpoint's state in `endpointState` (with seconds since its last dial in `idleSinceSeconds`):

| State | Meaning |
|---|---|
| `never_built` | lazy endpoint, no dial yet |
| `building` | a build is in progress (the budget wait included) |
| `up` | device built and awake |
| `asleep` | device built, Down (levels 1–2) |
| `torn_down` | device released (level 3, or evicted by the budget) |
| `down` | not started yet, or closed |
| `disabled` | switched off by hand (§12), whatever the device level underneath |

"Not built" is a state, not an error: the app shows "node not brought up" and does not treat it as a timeout.

---

## 12. A manual switch turns an endpoint off without a config change

The gRPC `SetEndpointEnabled(tag, enabled)` (`with_lx_command`, served by `lxd`) switches a WG/AWG endpoint off and on without a config change. The response carries the endpoint state after the call. LxBox calls it through libbox `CommandClient.SetEndpointEnabled`, which returns an `EndpointToggleResult` object (`State`).

- **Off:** an awake endpoint is suspended the same way the idle tick does it, so established connections through it are cut. Every dial, UDP listen and L3-forwarded packet is refused with `WireGuard endpoint is disabled`; nothing wakes it. The idle tick can still tear it down after `idle.teardown`, and the build budget can evict it.
- **On:** a suspended endpoint is woken at once (one handshake on the next packet). A torn-down or never-built one stays as it is and is built by the next dial, through the budget. If the wake fails, the call returns `Unavailable`; the endpoint is on but asleep and the next dial retries.
- **Not persisted:** a reload or `/admin/apply` creates every endpoint switched on.
- Works without `with_lx_idle_suspend`; on desktop a switched-off endpoint keeps its device, Down, until it is switched on or closed.

Errors: `NotFound` (no endpoint with that tag), `InvalidArgument` (not a WG/AWG endpoint), `FailedPrecondition` (core not started, endpoint closing), `Unimplemented` (build without `with_lx_command`).
