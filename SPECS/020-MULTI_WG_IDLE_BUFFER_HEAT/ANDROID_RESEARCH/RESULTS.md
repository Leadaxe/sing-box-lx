# Results — Android idle-suspend run

Device: **CPH2411**, Android **15** (SDK 35), **arm64-v8a**.
Core: **`1.14.0-lx.1-rc.18`** (confirmed via the app's `/device` core_version).
App: LxBox 2.8.0-dev.2 (build 2972).
Config: 9 WG endpoints (`wg-1` real+reachable=`final`, `wg-2..wg-9` synthetic+unreachable),
`route.lx_idle_suspend: "30s"` → tick 15 s.

Raw evidence in [`artifacts/`](artifacts/).

---

## 1. Behavioural matrix

| Check | Expectation | Result |
|---|---|---|
| **suspend fires** | 8 idle+unreachable endpoints go `Down`, one edge-triggered line each | ✅ `wg-2`..`wg-9` each logged `lx idle: suspend wg-N idle=30s` in the same tick |
| **reachable never suspends** | `wg-1` (the `final`, carrying live traffic) never sleeps | ✅ `wg-1` appears in **no** `lx idle:` line |
| **wake by=dial** | a dial through a sleeping endpoint wakes it | ✅ `POST /action/urltest?tag=wg-2` → `lx idle: wake wg-2 by=dial`; recv-workers 2→4 |
| **no flapping** | one suspend per node, no churn | ✅ suspend/wake pairs balanced; no repeated suspend after wake |
| **kill-switch** | `lx_idle_suspend: "0"` → tick never starts | ✅ **0** `lx idle:` lines over a 48 s idle hold |

### 1.1 Suspend — the actual log lines

From `artifacts/device-core-suspend.jsonl` (`lx idle:` lines, timestamps HH:MM:SS.mmm):

```
00:57:03.176  suspend wg-2 idle=30s
00:57:03.182  suspend wg-3 idle=30s
00:57:03.183  suspend wg-4 idle=30s
00:57:03.186  suspend wg-5 idle=30s
00:57:03.194  suspend wg-6 idle=30s
00:57:03.197  suspend wg-7 idle=30s
00:57:03.199  suspend wg-8 idle=30s
00:57:03.211  suspend wg-9 idle=30s
```

All 8 in a single tick, each `idle=30s` (edge-triggered on the tick that crossed the
threshold). `wg-1` — absent, as required.

### 1.2 Wake — the dial

```
00:59:14.262  wake wg-2 by=dial
```

Emitted the instant the single-node URLTest dialled through the sleeping `wg-2`; the
recv-worker count rose 2 → 4 (wg-2 re-opened its 2 workers). No spurious re-suspend
while the dial was live.

### 1.3 Kill-switch

With `route.lx_idle_suspend: "0"`, a 48 s idle hold produced **zero** `lx idle:`
lines — the idle tick does not start at all. The feature is a clean no-op when off.

---

## 2. Resource A/B — the headline

Clean `stop → start` (fresh **18** recv-workers, all endpoints `Up`), heap+goroutine
at T+~5 s, then again after the 8 synthetic endpoints suspended (recv-workers 18→2).

| Metric | Before (9 `Up`) | After (8 `Down`) | Δ |
|---|---|---|---|
| `RoutineReceiveIncoming` goroutines | **18** (2/endpoint × 9) | **2** (`wg-1` only) | **−16** |
| total goroutines | 417 | 389 | −32 |
| **`PopulatePools.func3` inuse_space** | **223.93 MB** | **89.89 MB** | **−134 MB (−60 %)** |
| total process `inuse_space` | 232.75 MB | 99.21 MB | −133.5 MB |

Raw pprof: `artifacts/device-heap-before.pb` / `device-heap-after.pb`; text renders in
`artifacts/heap-before-top.txt` / `heap-after-top.txt`; goroutine dumps in
`artifacts/device-goroutine-before.txt` / `device-goroutine-after.txt`.

### 2.1 `inuse_space` top — before (9 endpoints up)

```
Showing nodes accounting for 230.67MB, 99.11% of 232.75MB total
      flat  flat%   sum%        cum   cum%
  223.93MB 96.21% 96.21%   223.93MB 96.21%  wireguard-go/device.(*Device).PopulatePools.func3
    4.07MB  1.75% 97.96%     4.07MB  1.75%  sing/contrab/freelru.NewShardedWithSize[...natConn...]
    1.50MB  0.65% 98.61%     1.50MB  0.65%  runtime.allocm
    1.16MB   0.5% 99.11%     1.16MB   0.5%  sing/contrab/freelru.NewShardedWithSize[...]
```

### 2.2 `inuse_space` top — after (8 suspended)

```
Showing nodes accounting for 99.21MB, 100% of 99.21MB total
      flat  flat%   sum%        cum   cum%
   89.89MB 90.61% 90.61%    89.89MB 90.61%  wireguard-go/device.(*Device).PopulatePools.func3
    4.07MB  4.10% 94.71%     4.07MB  4.10%  sing/contrab/freelru.NewShardedWithSize[...]
    1.50MB  1.51% 96.23%     1.50MB  1.51%  runtime.allocm
    ...
```

### 2.3 Reading the numbers

`PopulatePools.func3` is the exact `make([]*[65535]byte, BatchSize)` allocation that
`RESEARCH.md` fingerprinted as the GC-heat / RAM holder. It **dominates** the heap in
both states (96 % before, 91 % after) — everything else on the profile is noise by
comparison. Suspending 8 idle+unreachable endpoints dropped it by **134 MB**.

Sanity check against the model:
- 8 endpoints × 2 recv-workers = 16 workers freed (matches goroutine 18→2).
- 134 MB / 16 ≈ **8.4 MB per worker** — matches the `~8 MB` estimate for `BatchSize=128`
  in `RESEARCH.md`.

So the on-device number is not just "big", it lands *exactly* where the source
analysis predicted. The desktop A/B (small `BatchSize`) could only show −31 % RSS
(~12 MB); on Android the same mechanism frees **~10× more**, because that is where the
`bufsArrs` are actually large. This is the platform the feature was designed for, and
the win shows up here at full size.

---

## 3. What this closes, what remains

**Closed (Android, on-device):** suspend, the reachability invariant (`final` stays
up under real traffic), wake-by-dial, no-flap, kill-switch, and — the point of the
whole exercise — a **directly measured** `bufsArrs` heap A/B. The device-verification
gap noted in `RESEARCH.md` §13 / `TEST_PLAN` is closed for Android, not just desktop.

**Still not measured (unchanged from `RESEARCH.md`, out of scope here):**
- **Battery / radio** A/B (stopping keepalive timers on sleeping nodes) — reasoned
  from source, not measured with `batterystats`.
- **Tier-B netstack teardown** — `Down()` does not free the gvisor `stack.Stack`
  (~5.9 MB/endpoint); it survives suspend by design. Still deferred.
- **Wake latency on far servers** — the desktop run measured the wake handshake cost
  (~+14–21 ms near / projected ~+450 ms first-packet on a ~150 ms-RTT node). Not
  re-measured on-device here; the mechanism is identical (Down zeroes keys → one
  handshake on the first packet after wake).
