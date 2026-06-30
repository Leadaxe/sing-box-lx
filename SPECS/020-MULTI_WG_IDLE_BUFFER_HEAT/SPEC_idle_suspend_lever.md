> ## ⚠️ Companion document — read [`SPEC.md`](SPEC.md) FIRST
>
> This is a **secondary** design doc for feature 020, NOT the authoritative spec.
> The authoritative [`SPEC.md`](SPEC.md) has **on-device proof** (heap A/B on a
> real OnePlus): the GC-scan heat holder is the **`bufsArrs` of recv-workers**
> (`make([]*[65535]byte, BatchSize=128)`, ~180 MB across `RoutineReceiveIncoming`
> on live WG devices), and its **PRIMARY lever is shrinking `StdNetBind.BatchSize()`**
> on android (128→8/16, one line in `conn/bind_std.go:322`) — simple, measured,
> no "activity" detection needed.
>
> **Where this doc diverges from SPEC.md (SPEC.md wins — it has measurements):**
> - This doc's source-reading guessed the GC holder was the *gvisor netstack
>   pointer graph*. SPEC.md **measured** it: the holder is wireguard-go `bufsArrs`,
>   not the netstack. Trust SPEC.md.
> - This doc claimed freeing buffers is pointless ("noscan"). SPEC.md is precise:
>   the `*[65535]byte` POINTERS in `bufsArrs` (a pointer array) + pointer-dense
>   `QueueElement`s ARE the scan cost, and their count ∝ number of devices — so
>   shrinking/freeing them **does** cut the heat. Trust SPEC.md.
>
> **What this doc still contributes:** a fully-worked design for SPEC.md's
> **"lever 3" (Down/suspend inactive devices)** — specifically a *light* variant
> (stop per-peer timers, keep keypairs/socket live, cheap wake, no handshake) plus
> the reachability-walk that decides *which* devices are inactive (idle AND
> unreachable from the active routing tree). SPEC.md flags lever 3 as the most
> expensive/complex option and prefers lever 1 (`BatchSize`); treat this doc as the
> detailed fallback design for lever 3 if/when it's needed, NOT a competing primary.
> Note: light-suspend (timersStop) does NOT free `bufsArrs` — only a real `Down`
> (BindClose → recv-worker exit, `receive.go:104-110`) or smaller `BatchSize` does.
> So for the *measured* holder, lever 1 or lever-3-deep (Down), not light-suspend,
> is what actually helps RAM/GC; light-suspend is the battery/CPU-timer win.

#### SPEC 020 (companion) — Idle WireGuard/AmneziaWG endpoint suspend — design for lever 3

Status: DRAFT (secondary design — see SPEC.md for the authoritative, measured plan).
Target: lx-1.14
Motivation memory: [[android-cpu-heat-multi-wg-gc]], [[awg-detour-guard-must-be-at-start]]
Cost facts in §1/§7.0 are source-verified against the wireguard-go submodule
(two adversarial passes) — but where they conflict with SPEC.md's on-device
measurements, SPEC.md is authoritative (see the banner above).

---

## 1. Problem

On Android the core heats up / pins CPU because every live WireGuard/AmneziaWG
endpoint runs a permanent background engine, and a separate per-endpoint
**gvisor `stack.Stack`** netstack. With N configured WG/AWG endpoints, all N run
24/7 **regardless of whether any traffic flows through them**. Two distinct
costs, established by source-verified investigation (do NOT conflate them):

- **Battery / CPU wakeups** — per-peer keepalive + handshake-retransmit timers,
  and (for AWG) junk-handshake machinery, fire on their own cadence even at idle,
  waking the radio and burning CPU.
- **GC scan heat** — the dominant scanner cost is NOT buffers (wireguard-go's
  `messageBuffers` are pointer-free *noscan* spans, marked O(1); the queue
  channels are nil-at-idle). It is the per-endpoint **gvisor `stack.Stack`
  pointer graph** (`transport/wireguard/device_stack.go:40`) — ~5 maps +
  demuxer + PortManager + IPTables — plus its **GOMAXPROCS TCP-dispatcher
  goroutines** (`tcp/dispatcher.go:406`) whose stacks are scanned every mark
  cycle. N live endpoints = N such graphs. This is [[android-cpu-heat-multi-wg-gc]].

Today the only way an endpoint stops is:

1. **Global pause** (`onPauseUpdated`) — `DevicePause` on screen-off; gates ALL
   endpoints at once via `device.Down()`, not selectively.
2. **`Close()`** — full teardown on core stop (this DOES release the netstack).
3. **AWG-detour-guard** — narrow correctness guard, not power saving.

There is **no notion of "this endpoint is currently unreachable from the active
routing tree"**, so no selective per-endpoint idle suspend. A non-selected
selector member, an out-of-pool urltest node, or an endpoint that is no rule
target and not final burns power forever.

Critical finding (§7.0): `device.Down()` is NOT cheap (full reconnect + fresh
handshake on wake) AND does **not** free the netstack graph — so plain `Down()`
neither resumes cheaply nor cuts the GC heat. The two costs need two mechanisms.

## 2. Goal & strategy (incremental, two tiers)

Selectively suspend any WG/AWG endpoint that is BOTH **unreachable** from the
active routing tree (not final, not a rule target, not the active choice of any
selector on an active path, not in any active urltest pool, not transitively
detoured-to by the above) AND **idle** beyond a threshold.

Delivered in tiers — **ship the safe one first, escalate later from field data**:

- **Tier A — light sleep (THIS SPEC, ships first).** `timersStop()` on the
  endpoint's peers: silence keepalive / handshake-retransmit / junk timers.
  Keypairs, socket, goroutines, and netstack all stay live → **wake is genuinely
  cheap (no handshake, no rebuild)**, hysteresis not critical. Cuts **battery /
  CPU wakeups / radio**. Does NOT cut the GC scan heat (netstack untouched) —
  honest partial win, lowest risk.
- **Tier B — netstack teardown (FUTURE, gated on field data).** The only tier
  that frees the dominant scan target: release the gvisor `stack.Stack` (≈
  `Close` of the wg device) for long-idle + unreachable endpoints. Wake = cold
  reconnect (rebuild stack + fresh handshake; in-flight flows die). Needs a
  longer threshold + hysteresis. Designed-for here (the tick is a ladder, Tier B
  bolts on as a deeper rung), NOT implemented in this SPEC.

The design is layered so Tier B is an additive deeper rung on the same idle tick
and `started`/`resumeMu` machinery — not a rewrite of Tier A.

Observability (ships WITH Tier A, §12a): INFO logs for every suspend/wake/skip
event — doubles as the field instrument that tells us whether Tier B is worth
building, what XX to pick, and whether flapping occurs.

Non-goals (this SPEC): Tier B implementation, tailscale suspend, urltest interval
tuning, freeing wireguard-go buffers (proven noscan — freeing them cuts neither
scan heat nor meaningfully helps, see §7.0).

## 3. Configuration

```json
"route": {
  "lx_idle_suspend": "30s"
}
```

- Field: `RouteOptions.LXIdleSuspend badoption.Duration` (lx:begin/lx:end), `json:"lx_idle_suspend,omitempty"`.
  This is **XX_light** (Tier A threshold).
- Default (absent / `"0s"` / `0`): **feature disabled** (kill-switch — safe rollback).
- `"30s"`: light-suspend endpoints idle ≥ 30s once unreachable.
- Tick period: `XX_light/2` (so 15s at default), clamped to a sane floor (e.g. ≥ 5s).
- Reserved for Tier B (future): `lx_idle_teardown` (XX_deep, must be ≫ XX_light);
  absent until Tier B ships. Not parsed in this SPEC.

## 3a. Constants

Everything Tier A introduces, in one place. ONE user-facing knob; the rest are
named internal constants (NOT magic literals) living in `route/reachability_lx.go`.

### User-facing (config)

| Name | Type | Default | Meaning |
|---|---|---|---|
| `route.lx_idle_suspend` | `badoption.Duration` | `30s` | **XX_light** — idle threshold. `0`/absent = feature OFF (tick never starts). |

### Internal (named consts, code-only)

| Const | Value | What it governs | Why this value |
|---|---|---|---|
| `idleTickDivisor` | `2` | tick period = `XX_light / divisor` | Sampling: to catch an endpoint crossing into "idle for XX" on time, poll about twice per XX window. Detection lag ≤ ~XX/2 instead of ~XX (divisor 1 = late, uneven detection; divisor 4 = 2× more wakeups for no real gain). The tick must be cheap *because* it is a power feature. |
| `idleTickFloor` | `5 * time.Second` | lower bound on tick period | Guardrail against a tiny configured XX. `period = max(XX/divisor, floor)`. Without it, `lx_idle_suspend:"2s"` → 1 s ticks → the reachability walk + endpoint scan runs ~60×/min, burning the very battery the feature saves. Floor caps poll frequency regardless of how small XX is. |

Effective tick period: `period = max(XX_light/idleTickDivisor, idleTickFloor)`.

| `lx_idle_suspend` (XX) | XX/2 | period = max(XX/2, 5s) | governed by |
|---|---|---|---|
| `30s` (default) | 15s | **15s** | divisor |
| `60s` | 30s | **30s** | divisor |
| `8s` | 4s | **5s** | floor |
| `2s` | 1s | **5s** | floor |
| `0` | — | tick not started | feature off |

Values are sensible defaults, not computed optima — divisor could be 3, floor
10s; revisit if field logs (§12a) show the tick itself is non-trivial. Tunable as
consts without config surface.

### NOT introduced in Tier A (deliberately)

- **No `XX_deep` / teardown threshold** — Tier B (reserved `lx_idle_teardown`).
- **No hysteresis / min-dwell const** — light flap is cheap (timer toggle, no
  handshake), and XX itself debounces (a dial stamps activity ⇒ no re-suspend for
  XX after any traffic). Hysteresis is a Tier-B concern (its flap = full reconnect).
- **No new WG-protocol constants** — light sleep lives ABOVE WG's existing timeouts
  (`RejectAfterTime=180s`, `KeepaliveTimeout=10s`, `RekeyAfterTime=120s`,
  `constants.go:16-23`); keypairs stay valid, so those are untouched. Note for
  Tier B: `DefaultURLTestInterval=3m` (`constant/timeout.go:14`) is the floor any
  future XX_deep must sit well ABOVE (else every probe forces a reconnect). For
  Tier A, XX(30s) < probe(3m) is fine — a probe just re-arms timers, no handshake.

## 4. Reachability model

`Router.ReachableOutbounds() map[string]bool` — the set of outbound tags
reachable from the **active** routing tree. NOT derivable from `ConsumersOf` /
`dependByTag`: that ledger is the *static* detour graph (a selector lists ALL
members as dependencies), whereas reachability needs the *current dynamic
choice*. So `ReachableOutbounds` is an independent walk.

### 4.1 Seeds (entry points into the tree)

- **Final**: `outboundManager.Default()`.
- **Rules**: every outbound tag referenced by a rule action (`route` / `bypass`)
  from `router.Rules()`. (Static — changes only on router rebuild.)

### 4.2 Downward walk from each seed

Visit transitively, with per-node type dispatch (cycle-guarded by a visited set):

| Node type            | Reachable children                          |
|----------------------|---------------------------------------------|
| selector             | **only** `Now()` (the active choice)        |
| urltest — pool (019) | **all** tags currently in `Pool()` / `poolTags()` |
| urltest — legacy     | `selectedOutboundTCP` + `selectedOutboundUDP` |
| ordinary (vless/…)   | `Dependencies()` (honest detour chain)      |

(Pool tags ARE reachable while in the pool — see §4.3 for why this needs no
carve-out and how suspend/wake stays homogeneous with user traffic.)

Collect every visited tag into the result map. An endpoint is **reachable** iff
its tag ∈ map.

### 4.3 urltest semantics — uniform "dial wakes, idle-tick sleeps" model

No special "pool = always reachable" carve-out. A urltest health-check probe
goes through the node's own `DialContext` (`urltest.URLTest(testCtx, link, p)`,
urltest.go ~511) — i.e. a probe is just an ordinary dial. So the SAME lazy
`Up()`-on-dial path that serves user traffic (§7) wakes a suspended node for its
probe automatically; no urltest-specific wake code.

Consequences:

- A node currently in the pool is probed every `interval`; each probe stamps
  `lastActivity`, so while `interval < XX` the node never goes idle → never
  suspended (desired: an actively-checked node stays live).
- A node evicted from the pool stops being probed; once it ALSO sits idle > XX
  it becomes a legitimate suspend candidate, suspended by the idle tick.
- **The health-check does NOT lower the node back down itself.** Lowering is the
  idle tick's job, by timeout only (§7). Synchronous down-after-probe would (a)
  drop a real user connection that arrived during the probe and (b) flap the WG
  handshake every `interval`. So: probe `Up()`s (via dial), probe finishes, node
  is simply released; the tick re-sleeps it XX s later if still idle+unreachable.

Net: pool membership needs no carve-out in the walk (§4.2 still descends into
the active pool because those tags are reachable *while in the pool*), but
suspend/wake is fully homogeneous with user traffic. The `setSlots` generation
bump (§5.1 #3) still stands — leaving/entering the pool changes the active tree,
so the cache must invalidate.

## 5. Cache + invalidation (counter / push-ish)

Recomputing the full walk every tick is wasteful (defeats the power goal). Cache
the walk; recompute only when the active selection actually changed.

### 5.1 Generation counters

Every point that mutates an active choice increments a monotonic
`atomic.Uint64` generation:

| # | Source                         | Type   | Where the `gen.Add(1)` goes                    |
|---|--------------------------------|--------|------------------------------------------------|
| 1 | `Selector.SelectOutbound`      | manual | next to `s.selected.Store(detour)`             |
| 2 | urltest legacy auto-switch     | auto   | where `selectedOutbound{TCP,UDP}` is reassigned |
| 3 | pool `setSlots` (SPEC 019)     | auto   | inside `balancer.setSlots` (lx file — free)     |
| 4 | router rebuild (reload/ruleset)| auto   | a `routerRebuildVersion` bump in Router         |

### 5.2 Aggregate generation

```
reachabilityGeneration() =
    routerRebuildVersion
  + Σ over all groups: group.Generation()
```

Monotonic by construction → any change strictly increases the aggregate (no
ABA). For paranoia, hash `(tag,gen)` pairs instead of summing; sum suffices at
our scale (tens of groups).

### 5.3 Cache read

```
ReachableOutbounds():
  cur = reachabilityGeneration()
  if cache.valid && cache.lastGen == cur: return cache.set   // 99% path, no walk
  set = walkReachable()
  cache = {set, lastGen: cur, valid: true}
  return set
```

In steady state (no UI taps, stable urltest) the tick is a cheap atomic compare —
the expensive walk runs only when reachability genuinely changed.

### 5.4 Rebase note

The `gen.Add(1)` in #1/#2 lands in **upstream** files (selector.go, urltest.go) —
a one-line insert per site, trivially re-applied but a potential merge touchpoint.
#3/#4 are in lx-owned code (free). Accepted trade-off per decision: counter form
chosen over pure-pull snapshot-hash. If merge pain appears, #1/#2 can be migrated
to pure-pull (read `Now()` outside, no upstream insert) without changing the
public contract.

## 6. Idle tracking

No per-endpoint idle counter exists today. Add one:

- `wireguard.Endpoint.lastActivity atomic.Int64` (unix nanos), stamped on each
  dial / new connection through the endpoint (`DialContext` / `NewConnection`).
- `IdleSince() time.Duration` helper.
- Suspend condition: `now - lastActivity > XX`.

## 7. Suspend / wake

### 7.0 COST REALITY — `Down()/Up()` is NOT cheap (drives the tiered design)

Adversarially verified against the wireguard-go submodule source. A
`device.Down()→Up()` cycle is a **full reconnect**, not a timer toggle:

`Down()` (`downLocked`) destroys:
- **All crypto keypairs + handshake state, zeroed** — `peer.Stop()` →
  `ZeroAndFlushAll()` → `DeleteKeypair` + `handshake.Clear()` (zeroes
  ephemeral/chainKey/hash, `noise-protocol.go:248`). Real zeroing, not a reset.
- **UDP socket closed** + detour conn closed (`client_bind.go:146`).
- **Staged packets discarded** (no retransmit).
- **2·N+1 goroutines torn down synchronously** (barrier `stopping.Wait()`).

`Up()` therefore pays a **full handshake** on the first packet: fresh Curve25519
keygen + 2× scalar-mult DH + BLAKE2s KDF/AEAD, **plus on this fork** the entire
AmneziaWG I1 obf chain + junk-packet CSPRNG fills (`send.go:135-154`). There is
**no session-resume path** in the Down/Up cycle.

What survives the cycle (`changeState`, not `Close`): the `Device`/`Peer`
objects, `ClientBind`, the buffer `WaitPool`s, the handshake/encryption/
decryption worker queues + goroutines, and the port. **Crucially `Down()` frees
NONE of the per-endpoint buffers that drive GC scan** — so plain `Down()` is
expensive on wake AND does not address the multi-WG GC heat
([[android-cpu-heat-multi-wg-gc]]). This is why §7.3 introduces a lighter tier.

Consequences (mandatory, not optional):
- **XX ≫ urltest probe interval.** If XX is near the probe cadence, every probe
  drives a full handshake — the opposite of saving work.
- **Hysteresis / min-dwell is REQUIRED.** `peer.Stop` early-returns if already
  down, so every borderline up↔down oscillation is a full-price flap.
  `RekeyTimeout=5s` only rate-limits duplicate initiations; it does NOT protect
  against suspend/wake thrash. Anti-flap state lives at the suspend layer (§7.4).

### 7.1 Endpoint state (shared by both tiers)

- `lightAsleep atomic.Bool` — Tier A state: timers stopped, everything else live.
  Distinct from `started` (which Tier B / guard toggle), so a light-asleep
  endpoint is still `started==true` and dials normally after a cheap re-arm.
- `resumeMu sync.Mutex` — serialises concurrent dial-wakes and mutually excludes
  wake vs the idle tick's sleep decision (both tiers).
- `lastActivity atomic.Int64` (§6). Stamp at PostStart so a never-dialed endpoint
  has a sane baseline (a zero value would read as ~55y idle → suspend on tick 1).
- `suspendedByGuard bool` — **Tier-B ONLY, NOT added in Tier A.** Tier A relies on
  the existing `started==false` (set by the guard's `device.Down`) instead; the
  `!started.Load()` check in `LightSuspendIfIdle` + the pre-existing `!started`
  dial gate fully cover the guard case without this flag.

### 7.2 Tier A — light suspend (THIS SPEC)

Light suspend stops the per-peer timer machinery via a new thin wrapper that the
endpoint exposes down to the wireguard-go device's peers, calling `timersStop()`
(`submodules/wireguard-go/device/timers.go:218` — stops retransmitHandshake,
sendKeepalive, newHandshake, zeroKeyMaterial, persistentKeepalive). It does NOT
touch `started`, the socket, keypairs, goroutines, or the netstack.

> Submodule note: `timersStop`/`timersStart` are unexported on `*Peer`. Tier A
> needs a small exported shim on the device (`Device.PauseTimers()` /
> `ResumeTimers()` iterating `peers.keyMap`) — lx-owned addition in the pinned
> submodule, under `lx:begin/lx:end`. **MUST hold `device.peers.RLock()` around
> the `keyMap` range** (mirroring `downLocked`, device.go:229-233) — else it races
> a concurrent peer add/remove (`SetPrivateKey`). The spec's earlier sketch
> omitted this lock; it is mandatory.
>
> Also note: `timersStart()` does NOT itself re-arm timers — it only zeroes
> handshake/keepalive counters. Re-arming happens lazily on the first data packet
> (`timersDataSent`/`timersDataReceived`), gated by `timersActive()` =
> `isRunning && device.isUp()`. In Tier A the device stays Up and `isRunning`
> true, so the first dial after wake auto-re-arms. `ResumeTimers()` is still
> correct to call (cheap, safe) but the *re-arm* is the dial's first packet, not
> the shim. The "no handshake on wake" guarantee holds because `timersStop`
> `DelSync`'d `zeroKeyMaterial` before it could zero the keypairs.

```go
// idle tick → light suspend. Cheap, reversible, NO handshake on wake.
// Silent on non-transition: early-returns and no-op CAS emit nothing (edge-triggered, §12a).
func (w *Endpoint) LightSuspendIfIdle(reachable bool, threshold time.Duration) {
    w.resumeMu.Lock()
    defer w.resumeMu.Unlock()
    if reachable || w.IdleSince() < threshold { return } // silent skip
    if !w.started.Load() { return }                      // guard-suspended (Down) → started==false; covers AWG-guard without a flag
    if w.lightAsleep.CompareAndSwap(false, true) {
        w.endpoint.PauseTimers() // timersStop on each peer; keypairs/socket/netstack intact
        w.logger.Info("lx idle: light-suspend ", w.Tag(), " idle=", w.IdleSince())
    }
}
```

Note: Tier A needs **no `suspendedByGuard` flag** (the spec's §7.1 lists it for
Tier B only). The AWG-detour-guard suspends via `device.Down` ⇒ `started==false`;
the `!started.Load()` check above means a guard-suspended endpoint is never
light-touched, and `resumeOnDial` (inserted AFTER the existing `!started` dial
gate) never resurrects it. So Tier A does not modify `started` or the guard.

Wake (Tier A) is **genuinely cheap** — re-arm timers, no handshake, no bind
reopen, no rebuild. The zeroKeyMaterial timer was stopped, so keypairs were NOT
zeroed during sleep (a key correctness point: light sleep must stop
`zeroKeyMaterial` so the session survives — `timersStop` already does).

### 7.3 Wake — lazy, in the dial gate, BEFORE first write

The existing dial gate (`DialContext` ~329, `ListenPacket`, `PrepareConnection`,
`NewDirectRoute…`) checks `!started.Load()`. Add a light-wake check alongside it.
Because Tier A keeps `started==true`, the bare gate would let the dial through on
a stale (timer-stopped) device; we must re-arm BEFORE the first write:

```go
// at the top of each dial entry, after the started check:
w.resumeOnDial()  // cheap; re-arms light sleep if needed, stamps activity

func (w *Endpoint) resumeOnDial() {
    w.stampActivity() // always, so the tick sees fresh activity
    if !w.lightAsleep.Load() { return } // fast path: not asleep
    w.resumeMu.Lock()
    defer w.resumeMu.Unlock()
    if w.lightAsleep.CompareAndSwap(true, false) {
        w.endpoint.ResumeTimers() // timersStart on each peer; synchronous, no network
        w.logger.Info("lx idle: light-wake ", w.Tag(), " by=dial")
    }
}
```

(A urltest health-check probe reaches the endpoint through `DialContext` too, so
it wakes via this same path — `by=dial` covers it, no separate `probe` source.
`devicewake` is the only other source: global screen-on `onPauseUpdated`.)

Ordering guarantee: `resumeOnDial` returns only after `ResumeTimers()`; the
caller then proceeds to the real dial → first write. Same goroutine, sequential —
the write cannot precede the re-arm. (`stampActivity` runs first and
unconditionally, closing the race with the idle tick: a dial immediately before a
tick makes the endpoint non-idle → no suspend.)

Global `DeviceWake` (screen-on) is unchanged; orthogonal to idle-suspend. For a
light-asleep endpoint, a global `DeviceWake` is harmless (timers re-arm on next
dial anyway), but `onPauseUpdated` should also `ResumeTimers()` to be safe.

### 7.4 Anti-flap (Tier A)

Light flap is cheap (timer toggles, no handshake), so strict hysteresis is NOT
required for Tier A — this is the main safety advantage of shipping light first.
A minimal guard: the idle threshold itself debounces (a dial stamps activity, so
re-suspend can't happen for XX after any traffic). The INFO logs (§12a) measure
real flap rate to inform whether Tier B needs stronger hysteresis.

### 7.5 Tier B — netstack teardown (FUTURE — designed-for, NOT in this SPEC)

The only tier that cuts the GC scan heat (§1), because it is the only one that
frees the gvisor `stack.Stack` graph + its TCP-dispatcher goroutines. Bolts on
as a deeper rung of the same idle tick: when `IdleSince() > XX_deep` (≫ XX_light)
AND still unreachable, escalate from light to teardown.

Mechanism (future): release the wg device + its netstack
(`stackDevice.Close()` → `stack.Close()` + `CleanupEndpoints()` + `Wait()`,
`device_stack.go:258`), which is bundled with `wgDevice.Close()`. Wake = cold
rebuild: `NewGVisorStackWithOptions` (new NIC/addresses/routes/forwarders) +
rebuild the wg `Device` + fresh handshake per peer; in-flight flows die.

Why it is only designed-for, not built, here:
- Wake is a true cold reconnect (handshake + AWG obf/junk, §7.0), so it needs a
  long XX_deep, real hysteresis / min-dwell, and acceptance that suspend kills
  live flows. That risk profile wants field data first.
- `device.Down()` (the existing `Suspend()` path) is the WRONG tool for Tier B's
  goal: it pays the full handshake cost on wake yet does NOT free the netstack
  (`downLocked` never touches `w.stack`). So Tier B is `Close`+rebuild, not
  `Down`. (The existing global-pause `Down()` stays as-is for screen-off.)

Tier B reuses `started`/`resumeMu`/`suspendedByGuard` (the guard already uses
`Down`-style suspend, so the §8 distinction matters for Tier B). The idle tick
becomes a ladder: `idle>XX_light → light`; `idle>XX_deep → teardown`. Tier A's
`lightAsleep` and Tier B's `started` are independent, so escalation is monotonic
(light first, then teardown) and de-escalation on dial restores the deepest level
needed.

## 8. Interaction with existing AWG-detour-guard

**Tier A and the guard do not collide** — they operate on different state, so no
`suspendedByGuard` flag is needed in Tier A (it is reserved for Tier B):

- The guard (`SuspendAmneziaWG`) suspends via `device.Down()` ⇒ sets
  `started==false`. A guard-suspended endpoint therefore:
  - is never light-touched — `LightSuspendIfIdle` early-returns on `!started`;
  - is never idle-woken — `resumeOnDial` runs *after* the pre-existing
    `!started.Load()` dial gate, which already returns "not ready" and short-
    circuits the dial before `resumeOnDial` executes. So the AWG-over-WG hang the
    guard prevents ([[awg-detour-guard-must-be-at-start]],
    [[masquerade-mechanism-i1-only]]) stays prevented.
- A light-asleep endpoint is still `started==true`, so if the guard later needs to
  `Down` it, that path works unchanged (light sleep only stopped timers).

`ConsumersOf`/`dependByTag` stays exclusively the guard's static walk; SPEC 020's
`ReachableOutbounds` is a separate dynamic walk and does not use it.

Tier B note: when teardown lands, it WILL toggle `started`-equivalent state like
the guard, so Tier B reintroduces a `suspendedByGuard` distinction (per §7.1/§7.5)
to keep idle-wake from resurrecting a guard-suspended endpoint. Out of scope here.

## 9. Edge cases / risks

- **First-packet RTT, not "wake latency"**: `Up()` is synchronous and non-network
  (bind up + handshake *initiated*), so resume itself is instant. The first
  packet after resume pays ~1 RTT while WG completes the handshake — WG stages
  that packet and flushes on key-ready. This is identical to a cold dial on a
  never-suspended endpoint or post-network-change reconnect; no warm-up wait, no
  special handling. Document the one-RTT first-packet cost. The 30s idle gate
  means it only hits genuinely cold paths.
- **dial vs idle-tick race (Tier A, the core correctness point)**: `resumeOnDial`
  (wake) and `LightSuspendIfIdle` (sleep) both decide under `resumeMu`, and the
  `lightAsleep` CAS makes the transition atomic. Outcomes are total:
  - dial wins → `stampActivity()` runs first (unconditional), then `ResumeTimers`
    if it was asleep; the tick then sees `IdleSince() < XX` (fresh stamp) → no
    suspend. Write proceeds on a re-armed device.
  - tick wins → `lightAsleep.CAS(false,true)` + `PauseTimers()`; the dial then
    takes the lock, sees `lightAsleep`, `ResumeTimers()`s. No lost wake — and even
    a missed re-arm only delays a keepalive, never drops the dial (timers are not
    on the dial data path; the socket/keypairs stay live in Tier A).
  Because `resumeOnDial` returns only after `ResumeTimers()`, and `stampActivity`
  is unconditional and first, the tick can never leave the device timer-stopped
  across a dial's first write. (Tier A flap is harmless regardless — no handshake.)
- **urltest pool churn**: entering the pool bumps generation → reachable next
  tick → not suspended; the per-`interval` probe also stamps activity, so a
  pool node with `interval < XX` never goes idle. Leaving the pool makes a node a
  candidate only after it ALSO sits idle XX s. No flap when XX ≫ tick period.
- **Selector → sub-selector chain**: walk is transitive via `Now()`; only active
  choices are followed. Correct.
- **Rule referencing a group**: seed is the group tag; walk descends into its
  active choice / current pool. Correct.
- **Guard-suspended endpoint (Tier A)**: `LightSuspendIfIdle` early-returns on
  `!started` (the guard's `Down` sets `started==false`) — never light-touched; and
  a light-asleep endpoint is still `started`, so the guard's own `Down` works on it
  unchanged. No `suspendedByGuard` flag needed in Tier A. No interference.
- **Disabled (XX=0)**: tick never scheduled; zero overhead; current behaviour.

## 10. Files (anticipated — confirmed at plan stage)

New (lx-owned):
- `route/reachability_lx.go` — `ReachableOutbounds`, walk, cache, generation aggregation.
- spec/changelog entry in `docs-lx/lx-changelog.md` (header `#### v<tag>`).

Modified (Tier A — light):
- `option/route.go` — `LXIdleSuspend` field (lx:begin/lx:end).
- `route/router.go` — idle tick, reachability cache state, `routerRebuildVersion`,
  wire `Rules()`/`Default()`; iterate light-suspendable endpoints per tick.
- `protocol/group/selector.go` — `gen` counter + `Generation()`; bump on `SelectOutbound`. *(upstream touch)*
- `protocol/group/urltest.go` — `gen` counter + `Generation()`; bump on legacy auto-switch. *(upstream touch)*
- `protocol/group/urltest_balance_lx.go` — bump generation in `setSlots`. *(lx file)*
- `adapter/outbound.go` — `Generation()` on OutboundGroup (or a narrow `Generational` interface);
  narrow `LightSuspendable` interface (`LightSuspendIfIdle`, `Tag`) so the Router tick iterates
  WG/AWG endpoints without importing the concrete type.
- `protocol/wireguard/endpoint.go` — `lastActivity`, `IdleSince()`, `stampActivity()`,
  `lightAsleep`, `resumeMu`, `LightSuspendIfIdle()`, `resumeOnDial()`; add `resumeOnDial()`
  call at the top of dial gates (`DialContext`/`ListenPacket`/`PrepareConnection`/
  `NewDirectRoute…`). Tier-A does NOT modify `started` or `SuspendAmneziaWG`.
- `submodules/wireguard-go/device/device.go` — `Device.PauseTimers()` / `ResumeTimers()`
  exported shims iterating `peers.keyMap` calling each peer's `timersStop()`/`timersStart()`.
  *(lx-owned addition in pinned submodule, lx:begin/lx:end — keep minimal)*

Deferred to Tier B (FUTURE, not this SPEC): `started`/`suspendedByGuard` wake path,
`device.Down`-vs-`Close` escalation, `XX_deep`, netstack teardown/rebuild, stronger
hysteresis. The §7.5 design reserves the seams.

## 11. Test plan (Tier A)

- Unit: `ReachableOutbounds` over hand-built topologies — selector (only Now),
  pool (all poolTags), legacy urltest, rule seed, final seed, detour chain,
  cycle guard.
- Unit: generation cache returns cached set when gen unchanged; recomputes when a
  group bumps.
- Unit: idle math (`IdleSince`), light-suspend predicate boundaries, `lightAsleep`
  CAS transitions, dial-vs-tick race (resumeOnDial stamps before tick reads).
- Unit/integration: a light-asleep endpoint dials correctly with NO handshake
  (assert keypairs survived — zeroKeyMaterial timer was stopped, not fired).
- **Live `box.New` run** (not direct-unmarshal — [[badjson-empty-slice-collapses-to-nil]]):
  config decode of `lx_idle_suspend`; a suspend→dial→wake round-trip that does
  NOT re-handshake (Tier A) and still passes traffic.
- Device: measure with the INFO logs (§12a) — light-suspend/wake counts, flap rate,
  and CPU/keepalive-radio drop with N idle WG endpoints. (GC scan heat is expected
  to persist — that is Tier B; confirm via on-device heap/allocs pprof per
  [[lxbox-207-pprof-capture]] / [[android-cpu-heat-multi-wg-gc]] before deciding Tier B.)

## 12a. Observability — INFO event log (ships WITH Tier A)

**Edge-triggered only — log STATE TRANSITIONS, never per-tick decisions.** The
idle tick runs every ~XX/2 over every endpoint; logging what it *considered*
(reachable / not-idle) would spam INFO. So each log sits INSIDE the successful
CAS that flips state, firing exactly once per real transition:

- `lx idle: light-suspend <tag> idle=<dur>` — emitted inside `lightAsleep.CAS(false→true)`.
- `lx idle: light-wake <tag> by=<dial|devicewake>` — emitted inside `lightAsleep.CAS(true→false)`.
- (Tier B, when built) `lx idle: teardown <tag>` / `lx idle: rebuild <tag> cost=<dur>`.

NO `skip` logs: a tick that re-considers an already-asleep or still-reachable
endpoint is silent (its CAS no-ops). Both events are rare (only transitions) so
both stay at INFO — no level tuning needed. A suspend↔wake pair in the log IS the
flap signal, read directly. From these we get real flap rate, suspend coverage,
and wake cadence — the inputs for the Tier-B decision and XX tuning. A counter
snapshot (suspended N / live M) on the command stream is a nice-to-have, not
required for Tier A.

## 12. Open questions (resolved)

- XX default & form → `route.lx_idle_suspend` Duration, 30s, 0=off. ✓
- Tick location → Router. ✓
- Invalidation → generation counter (push-ish). ✓
- urltest members → suspend only if NOT in current pool. ✓
- Down/Up cost → NOT cheap (full reconnect + handshake), source-verified. ✓
- GC heat source → gvisor netstack graph, NOT buffers; `Down()` doesn't free it. ✓
- Scope → Tier A light (timersStop) ships first; Tier B teardown designed-for,
  deferred to field data from §12a logs. ✓
- INFO event logging → ships with Tier A as field instrument. ✓

## 13. IMPLEMENTATION LOG (lx-spec020-idle-suspend branch)

This section is the as-built record. It SUPERSEDES the Tier-A "light sleep"
framing above where they differ: source-verified measurement (SPEC.md) proved the
GC/heat holder is the recv-worker `bufsArrs`, and light sleep (`timersStop`) does
NOT free it. So the shipped mechanism is **Down/Up (deep)**, not light sleep.

### 13.1 What shipped (commit c55cf11e)

idle-suspend via `device.Down()` / `device.Up()`:
- `route.lx_idle_suspend` (Duration, 0 = off).
- `ReachableOutbounds` walk (final + rule targets → selector `Now()` / urltest
  active pool `ActiveTags()` / static detour deps). Fresh walk per tick, no
  generation cache (graph is tiny, tick is ~XX/2; a cache would need invalidation
  hooks inside upstream selector/urltest bodies — not worth the rebase cost yet).
- Router idle tick, period `max(XX/2, 5s)`, started in PostStart / stopped in Close.
- Endpoint `SuspendIfIdle` (Down on live→asleep CAS) + `resumeOnDial` (stamp + lazy
  Up on next dial). `idleAsleep` kept distinct from `started` so a guard-suspended
  endpoint is never idle-woken.
- INFO log on each state transition only (edge-triggered): `suspend` / `wake`.

Effect: a WG/AWG endpoint that is idle past XX AND unreachable from the active
routing tree is brought Down → its recv-worker `bufsArrs` is freed → the dominant
GC-scan holder shrinks. Wake (next dial) re-opens the socket and pays a fresh
handshake (Down zeroed the crypto session).

### 13.2 Bind-swap / key-zeroing — investigated (the promised comment)

**Can the bind be resized on a LIVE endpoint WITHOUT zeroing keys? YES — via
`Device.BindUpdate()` (`submodules/wireguard-go/device/device.go:507`).** It only
closes+reopens the socket and re-spawns recv-workers; it never calls `peer.Stop()`
/ `ZeroAndFlushAll()`. Key-zeroing lives ONLY in `peer.Stop()`, reached only from
`downLocked()` (= `Device.Down()`) and peer removal. So a bind swap that keeps keys
is possible — but ONLY on a still-Up device; once `Down()` has run, keys are
already gone and `BindUpdate` can't bring them back.

This means the shipped Down path (13.1) cannot have a keys-safe wake — by
construction. A keys-safe / reduced-bind wake needs a different suspend side.

### 13.3 The three paths (reduced-bind urltest wake) — for the next iteration

Recorded so the decision isn't re-derived. `bufsArrs` is sized from
`device.BatchSize() = max(bind, tun)` (`device.go:368`); the GRO read path panics
with batch<128 unless GRO is also disabled (`bind_std.go:72/261/292` — msgsPool is
fixed 128, the consume loop is unclamped). So "8 buffers, no GRO" — both halves are
load-bearing.

| | B — Down (SHIPPED) | A — BindUpdate + reduced bind | Hybrid |
|---|---|---|---|
| sleeping-node RAM | **0** | ~0.5 MB (8 bufs) | 0.5 MB short / 0 long |
| wake handshake | yes | **no** (keys live) | no / yes |
| urltest probe handshake | yes | **no** | no |
| readiness | **done** | new code | most code |
| risk (GRO panic, max(bind,tun) trap) | none | real | real |

- **B (Down)** — what shipped. RAM to zero, simplest, safe. Wake pays a handshake
  (only the idle node, only on wake — rare/cheap). A reduced-bind probe wake on
  path B saves only the probe's RAM, NOT the handshake (keys already zeroed), so it
  is low value here.
- **A (BindUpdate, never Down)** — keep the device Up, swap to an 8-buffer / GRO-off
  bind via `BindUpdate`. Keys live → wake (and urltest probe) pay NO handshake.
  RAM not zero (~0.5 MB) and three sharp traps: mutable `BatchSize()` wrapper; must
  shrink TUN BatchSize too or `max()` clamps back to 128; must open with rxOffload
  OFF or batch<128 panics.
- **Hybrid** — two idle thresholds: short idle → reduced bind (keys live), long idle
  → full Down (RAM 0). Most complete, most code.

### 13.4 Recommendation (ship B, escalate on data)

Ship **B** (done): it hits the measured holder, cuts heat to zero, lowest risk.
Defer **A/Hybrid** until the §12a INFO logs from a device run show handshake
flapping on wake actually hurts — then escalate with data, not speculation. The
reduced-bind urltest wake (the original task-6 ask) only delivers its full value
(no-handshake probe) on path A/Hybrid; on B it is low-value, so it is deferred with
a TODO referencing this section.
