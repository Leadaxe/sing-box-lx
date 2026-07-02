# A/B result — idle-suspend OFF → ON, real user config

The before/after on a **real production LxBox setup** (not synthetic): 11
WireGuard/AmneziaWG endpoints, only ~1 carrying traffic, the rest idle. Captured on
2026-07-01 on a physical CPH2411 (Android 15, arm64), core `1.14.0-lx.1-rc.18`,
through the app's Debug API pprof passthrough.

- **Before** (`route.lx_idle_suspend` absent → feature OFF): [`README.md`](README.md) + files in this folder.
- **After** (`route.lx_idle_suspend: "30s"` → feature ON, endpoints slept): [`after/`](after/).

## The headline — CPU collapses by ~76×

| Metric | OFF (before) | ON (after) | Δ |
|---|---|---|---|
| Live WG endpoints | 11 | **1** | −10 |
| `RoutineReceiveIncoming` (recv-workers) | 22 | **2** | −20 |
| **`PopulatePools.func3`** inuse_space (messageBuffers pool) | 263.29 MB | **107.98 MB** | **−155 MB (−59 %)** |
| `func3` inuse_objects | 4212 buffers | **1726 buffers** | −2486 |
| total heap inuse_space | 276.91 MB | 126.63 MB | −150 MB |
| **CPU (process)** | **259.64 %** | **3.40 %** | **−256 pp (~76× less)** |
| **`runtime.scanobject`** | **56.06 % cum CPU** | ~3 % (out of the top) | GC scan work ~gone |
| Native Heap (dumpsys) | 36.6 MB | 31.2 MB | −5.4 MB |
| Tunnel state | connected, traffic OK | **connected, traffic OK** | unaffected |

Raw: before in [`heap-inuse_space.txt`](heap-inuse_space.txt) /
[`cpu-profile-top.txt`](cpu-profile-top.txt); after in
[`after/heap-inuse_space.txt`](after/heap-inuse_space.txt) /
[`after/cpu-profile-top.txt`](after/cpu-profile-top.txt). `.pb` files in both for
`go tool pprof`.

## Why this is the strongest evidence in the whole SPEC

The desktop A/B measured memory (−31 % RSS). The synthetic Android A/B measured the
`bufsArrs`/messageBuffers heap holder (224→90 MB). **This run measures the causal
chain end-to-end on a real config:**

1. **Before, at idle:** 11 live WG hold **263 MB** of message buffers
   (`PopulatePools.func3`), and **`runtime.scanobject` is 56 % of CPU** — the GC is
   burning 2.6 CPU cores scanning those buffers, with no useful traffic. This is
   exactly the heat pathology RESEARCH.md described, now observed directly.

2. **After idle-suspend:** the 10 idle+unreachable endpoints go `Down`, the buffer
   pool collapses to **108 MB**, and **CPU drops from 260 % to 3.4 %** — `scanobject`
   falls out of the profile top entirely (what remains is ordinary syscalls/futex).

So the memory saving (−155 MB) and the **CPU/heat saving (−256 pp)** are measured
together, on the user's own subscription, with the tunnel's live traffic path
untouched. The "device doesn't feel hot" was a perception gap: the phone *was*
burning 2.6 cores on GC; idle-suspend removes it.

## Live wake↔suspend cycle (a mass-ping, then re-sleep)

`after/lx-idle-massping-cycle.jsonl` captures a real cycle: a manual mass-ping
(URLTest of all nodes) dialled through the sleeping endpoints and **woke 7 of them**
(`wake … by=dial`), then — once they went idle + unreachable again — the idle tick
**re-suspended all 7** ~41 s later (`suspend … idle=41s`):

```
06:46:20  wake  WARP-2/3/4/5/6/7 + WARP   by=dial     (mass-ping dialled each)
06:47:02  suspend WARP-2/3/4/5/6/7 + WARP idle=41s    (idle tick put them back down)
```

**wake = 7, suspend = 7 — perfectly balanced, no flapping.** This confirms the
"dial wakes, idle-tick sleeps" contract (SPEC §4.3) on a real device: any dial
(mass-ping, health-check, or real traffic) transiently wakes a node; if it then sits
idle and off-route, it is re-suspended after the threshold. A mass-ping is a one-shot
spike, not a permanent wake — CPU/RAM briefly rise, then collapse again.

## Config for both captures (scrubbed)

[`config-scrubbed.json`](config-scrubbed.json) — 11 WG endpoints, `route.final =
vpn-1` (a selector over a round_robin urltest pool). All credentials redacted.

## Reproduce the delta

```
# before
go tool pprof -top -inuse_space heap.pb        | grep PopulatePools   # 263.29 MB
go tool pprof -top cpu.pb                       | grep scanobject      # 56.06% cum
# after
go tool pprof -top -inuse_space after/heap.pb  | grep PopulatePools   # 107.98 MB
go tool pprof -top after/cpu.pb                 | grep Duration        # 3.40% total
```

## How to reproduce the run (UI path)

Enable the feature: **VPN Settings → System → Optimization → Suspend idle tunnels →
30 seconds**, then reconnect (`stop → start`). Wait past the threshold for the
off-route endpoints to sleep, then capture pprof. (During this study the threshold
was also set directly via the Debug API `PUT /config` — identical effect; the storage
key is `route_idle_suspend`, the config field `route.lx_idle_suspend`.)
