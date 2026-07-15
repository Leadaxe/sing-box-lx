# sing-box-lx — energy optimization on mobile (idle-suspend × urltest)

> 🌐 Русская версия: **[lx-energy.ru.md](lx-energy.ru.md)**.
>
> Mechanism specs: [SPEC 020](../SPECS/020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md) (idle-suspend), [SPEC 019](../SPECS/019-URLTEST_MODE_STICKY/SPEC.md) (round_robin/pool/passive_check), [SPEC 007](../SPECS/007-AWG_OVER_WIREGUARD_DETOUR_GUARD/SPEC.md) (AWG guard). Config keys of all lx features: [lx-config.md](lx-config.md).

This is the main document on **why the fork saves battery on Android and how to control it**. Upstream sing-box keeps every WireGuard/AmneziaWG endpoint alive 24/7 regardless of traffic: recv-workers with their buffers (~8 MB per worker at the mobile `BatchSize=128` — the dominant GC-heat source; measured on-device: 8 endpoints suspended freed 134 MB), plus keepalive/handshake timers that wake the radio. The fork adds selective **suspension** of idle endpoints and teaches the health-check machinery **not to keep them awake**. The full model, step by step, follows.

---

## 1. The big picture: three layers

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
│ LAYER 3 · THE ENDPOINT (device Down/Up)                         │
│ Down: workers exit, buffers freed, timers silent                │
│ Up (dial-only): +1 handshake RTT on the first packet            │
└─────────────────────────────────────────────────────────────────┘
```

Separate from all this lives the **urltest health check** (probes). It is not part of the suspend machinery, but every probe is an ordinary dial and therefore **wakes** sleeping endpoints. Half of the optimizations are about making probes run only when genuinely needed (§6).

## 2. The reachability layer

An endpoint is **reachable** if a new connection can land on it right now. Walk seeds (entry points):

- `route.final` (the default outbound);
- the target of every route/bypass rule;
- **every DNS server's detour** — DNS is dialed on every resolution, so a DNS-only node is reachable by definition (otherwise it would flap Down/Up around every quiet gap, adding a handshake to the first DNS query of each session).

Descent: a selector → its current choice only (`Now()`); a round_robin urltest → its **whole current pool**; a legacy urltest → the currently selected node; an ordinary outbound → its detour chain. Everything is transitive; cycles are cut.

The set is cached and recomputed **only on events** (selector switch, urltest auto-switch — including the very first selection, pool rebuild, config reload). Between events the tick reads the ready map — one map lookup plus a couple of atomics per endpoint per tick.

## 3. Two idle thresholds

```jsonc
"route": {
  "lx_idle_suspend": "30s",            // threshold for UNREACHABLE endpoints (feature switch)
  "lx_idle_suspend_reachable": "30m"   // optional: threshold for REACHABLE endpoints
}
```

| | Unreachable endpoint | Reachable endpoint |
|---|---|---|
| Who | not selected, not pooled, not in any rule | pool member, selected node, final, DNS detour |
| Idle window | `lx_idle_suspend` (short, 30s) | `lx_idle_suspend_reachable` (long, 30m); `0`/absent — never suspends |
| Rationale | traffic cannot land on it — sleep immediately | traffic may arrive any moment; suspend only after long silence, paying +1 RTT on wake |

Validation: the reachable threshold requires `lx_idle_suspend` and must be `>=` it. Recommendation: `>=` the `idle_timeout` of your urltest groups (explained in §7).

"Idle" = time since the last **dial** through the endpoint (new-connection creation). Data on already-established connections bypasses the dial path — gates §4.5–4.6 protect it.

## 4. The tick decision: gate chain

Every tick, for every endpoint, cheapest-first:

1. **Listen mode** (`listen_port` set) → never suspend: inbound peers have no way to wake it.
2. **Window selection**: unreachable → `lx_idle_suspend`; reachable → `lx_idle_suspend_reachable` (or bail out if unset).
3. **Idle clock**: `IdleSince() < window` → too early.
4. **Already down** (guard/closed) → someone else's state, don't touch.
5. **Live TCP flows**: the device gVisor stack's `CurrentEstablished` gauge > 0 → established connections exist (a download, a push socket) → don't suspend. Precise and **keepalive-immune** (a keepalive is not a TCP flow).
6. **Counter delta**: rx+tx grew by **≥ 4096 bytes** since the previous decision → live UDP/QUIC traffic → refresh the clock, don't suspend. A threshold rather than a bare "changed" check on purpose: WG keepalive (32 B/interval) and rekeys (~240 B/~2 min) move the counters forever — a `persistent_keepalive` peer must still be able to sleep (silencing its timers is half the win). A fully silent QUIC flow (rare pings below the threshold) will be suspended — an accepted trade-off; QUIC migrates/re-establishes.
7. All quiet → `device.Down()`, log `lx idle: suspend <tag>`.

## 5. Down, Up, and the wake cost

`Down()` is a freeze, not a Close: the UDP socket closes (recv-workers exit and release their buffers — the main RAM/GC win), session keys are zeroed, and **all** peer timers stop (keepalive, retransmit, the whole AWG junk machinery). Objects and the port survive the cycle.

Waking has **exactly one path**: a real dial through the endpoint (`DialContext`/`ListenPacket`/L3 forwarding). It re-opens the socket and workers; the first packet waits for a fresh handshake — **+1 RTT** (14–21 ms on a nearby server, up to ~450 ms intercontinental). Throughput is unaffected.

> **Why the +1 RTT is here to stay.** The theoretical alternative — waking with the crypto session intact (`BindUpdate` leaves the keys alone) — is **rejected**: a WireGuard session lives 120 s and is invalid past 180 s while we sleep for minutes, so the handshake happens anyway; and on mobile a network change (Wi-Fi↔LTE) during sleep makes the endpoint the server knows stale, turning the "saving" into a timeout instead of an honest handshake on CGNAT paths. Full reasoning: SPEC 020 §"ОТВЕРГНУТО: путь A".

Wake nuances (frequently asked):
- **The attempt itself wakes, not its success.** The wake happens at dial entry, before anything is sent; if the connection then fails (target unreachable), the tunnel is already awake and will fall asleep again after its window. "Any app" = any app whose traffic is routed into this tunnel; apps going direct/through another channel don't wake it.
- **An open TCP socket keeps the tunnel awake entirely** (the established-flows gate, §4.5) — so the "app writes into a socket while the tunnel already sleeps" case does not exist for TCP. A silent UDP/QUIC session may fall asleep; QUIC then re-establishes with a fresh dial — which is exactly what wakes the tunnel.

What does **not** wake an endpoint:
- screen-on / network change (pause-wake): the transport remembers a `suspended` flag and skips such devices — otherwise one screen cycle would resurrect everything and desync the bookkeeping until restart;
- a guard-suspended AWG endpoint (see SPEC 007) is resurrected by nothing short of a core restart — that is the AWG-over-WG kernel-hang protection.

For domain destinations, DNS resolution happens **before** the wake — a failed lookup does not pay a handshake. **This is safe — a sleeping tunnel cannot "take DNS down"**: resolution never depends on the sleeping tunnel blindly. Ordinary DNS doesn't touch it at all (a failed lookup would mean DNS is broken regardless of the tunnel's state), and a DNS server detoured *through* this very tunnel sends its query as a separate dial to the server's IP — and that dial **wakes the tunnel itself**. The "asleep, therefore can't resolve" deadlock is impossible by construction.

## 6. urltest: probes, and how they were taught to stay quiet

A health-check probe is an ordinary dial through the node — i.e. it **wakes** a sleeper. The mechanisms bounding probes:

| Mechanism | Level | Effect |
|---|---|---|
| `idle_timeout` (upstream, 30m) | group | no traffic through the group for longer → the probe ticker **stops for good**; the next dial (`Touch`) restarts it and runs an immediate re-test |
| Reachability gate (lx) | group | group unreachable (selector left) → cycles are **skipped** immediately, without waiting for idle_timeout; the ticker stays alive and self-recovers |
| `passive_check` (lx, opt-in) | node/cycle | a successful TCP dial through a node proves liveness (the SYN/SYN-ACK traversed the whole chain); while fresh (< interval): least_test skips whole cycles, round_robin skips confirmed slots |
| round_robin pool (lx) | mode | only pool members are probed (e.g. 3), not all N nodes; out-of-pool nodes only when refilling holes |
| Manual test | — | always full (force), never gated |

An important consequence for an active least_test group: **without** `passive_check` it probes every member each `interval`, waking sleepers (upstream semantics — picking the best requires measuring everyone). With `passive_check`, probes simply don't run while traffic itself proves the selection healthy.

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

The "probe wakes a falling-asleep node" race cannot happen by construction: probes go silent at `T = idle_timeout`, reachable-sleep starts no earlier than `T = last probe + reachable`. With `reachable >= idle_timeout` the alarm clock is already dead by bedtime. (Violating the recommendation is not breakage: 1–2 flap cycles in the tail.)

### Selector switching away from a group

```
T=0   the selector moves off group AUTO
      → InvalidateReachability: AUTO and its members become unreachable
T=0+  AUTO's scheduled probe cycles are SKIPPED (reachability gate)
~30s+ AUTO's members fall asleep on the SHORT threshold (they are unreachable)
T=30m idle_timeout retires AUTO's ticker for good
```

Before this revision an abandoned group kept probing (and waking) all members for another 30 minutes — up to 10 cycles × N handshakes for nothing.

## 8. Recommended mobile configuration

```jsonc
{
  "route": {
    "lx_idle_suspend": "30s",
    "lx_idle_suspend_reachable": "30m"
  },
  "outbounds": [{
    "type": "urltest",
    "tag": "auto",
    "outbounds": ["node-1", "…", "node-N"],
    "interval": "15m",          // rarer cycles where they still run
    "idle_timeout": "30m",      // = the reachable threshold (see §7)
    "passive_check": true,      // traffic itself confirms liveness — probes stay quiet
    "mode": "round_robin",      // the pool is probed, not all N
    "balancer": { "pool": 3, "pool_tolerance": 0 }
  }]
}
```

Threshold coordination rules:

| Constraint | Enforced by | Consequence of violation |
|---|---|---|
| `interval <= idle_timeout` | core (upstream), start error | — |
| `lx_idle_suspend_reachable >= lx_idle_suspend` | core (lx), start error | — |
| `lx_idle_suspend_reachable >= idle_timeout` of groups | recommendation | 1–2 probe flaps in the falling-asleep tail |
| a low reachable value (e.g. `5m`) | allowed | fast sleep during pauses; the cost is the same 1–2 extra handshakes during the first ~half hour of silence (until probes retire) plus +1 RTT on the first connection after every ≥5m pause |
| `interval` > MASQUE idle (5m) for groups with MASQUE nodes | recommendation | probes keep the MASQUE tunnel from sleeping |
| `pool_tolerance <= 15000` | core (lx), start error | — |

Notes:
- `pool_tolerance > 0` is discouraged on mobile: that mode must measure **all** candidates every cycle (waking every sleeper outside the pool).
- `persistent_keepalive` on peers is compatible with suspend: the timer is stopped while asleep, and keepalive noise does not block falling asleep (§4.6). An awake keepalive node keeps waking the radio — that is the user's choice.
- The whole feature is disabled by omitting `lx_idle_suspend` (kill switch, zero overhead) and exists only in the mobile AAR (`with_lx_idle_suspend`); a desktop binary given this key fails fast at start with an explicit error.

## 9. Guarantees (what will NOT break)

- **Live connections are never cut**: a node carrying an established TCP flow or bulk UDP traffic is not suspended, however "idle" it looks by dials (§4.5–4.6). The `interrupt_exist_connections=false` promise holds.
- **Screen-off/on and network changes don't desync anything**: suspended devices stay suspended through pause/wake cycles.
- **The AWG guard outranks idle logic**: a guard-downed AWG endpoint is woken by nothing — not a dial, not pause, not a probe.
- **The first request after sleep always works**: it goes through the last known node (waking it), while an immediate re-test refreshes the selection in parallel. If that node died overnight — one request fails and the re-test repairs the pool right away.
- **Kill switch**: without `lx_idle_suspend` not even the tick starts.

## 10. Observability and troubleshooting

- `lx idle: suspend <tag> idle=…` / `lx idle: wake <tag> by=dial` — edge-triggered pairs in the log. **Frequent suspend↔wake pairs for one tag = flapping** — check threshold coordination (§8) and that the probe gates are active.
- A sleeping node = 0 recv-workers for its device (goroutine profile) and zero rx/tx.
- A node "won't sleep although it should": is it reachable? (selector/pool/rule/DNS detour — §2); does it carry established TCP? bulk UDP?
- A node "won't wake": see SPEC 007 — is it a guard-downed AWG? (`amneziawg endpoint suspended/will not start` in the log).
- Probes "run at night": is there traffic through the group (`Touch` keeps the ticker alive)? Remember that background push connections count as traffic too.
