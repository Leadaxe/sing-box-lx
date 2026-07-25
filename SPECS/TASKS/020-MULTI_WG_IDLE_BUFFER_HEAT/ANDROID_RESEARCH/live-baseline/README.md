# Live baseline — idle-suspend OFF, real user config

Full metric snapshot of a **real production LxBox setup** with **idle-suspend
disabled**, captured on 2026-07-01 ~09:34 MSK. This is the "before" point for a
future "after" (idle-suspend on) comparison, and — more importantly — it directly
demonstrates the *cost* the feature is meant to remove, on a genuine config, not a
synthetic one.

All credentials scrubbed: private keys, pre-shared keys, public keys, tokens, WARP
account, proxy user/pass, subscription URLs and LAN IPs are replaced with
`<redacted*>` in `config-scrubbed.json`. The pprof `.pb` and goroutine dumps contain
only memory addresses and Go package paths.

## Device / build

| | |
|---|---|
| Device | CPH2411, Android 15 (SDK 35), arm64-v8a |
| App | LxBox 2.8.0-dev.2 (build 2972) |
| Core | `1.14.0-lx.1-rc.18` |
| Battery optimisation ignored | yes |
| App uptime at capture | ~5.7 h |

## Config shape (the real subscription, scrubbed)

Full scrubbed config: [`config-scrubbed.json`](config-scrubbed.json).

- **11 WireGuard/AmneziaWG endpoints**, all `Up`:
  `WARP (AWG 1.5)` ×6, `WARP` (plain) ×1, `home`, `awg2-home`, `wg-parnas`, +1.
- outbounds: 55 vless, 32 socks, 4 selector, 1 urltest, direct, block.
- `route.final = vpn-1` (a selector containing a **round_robin urltest `pool:4`**).
- **`route.lx_idle_suspend` — absent → the feature is OFF.**

So: 11 WG endpoints are live, but traffic only flows through the small active pool in
`vpn-1`. The other endpoints sit idle yet fully `Up`.

## The numbers — idle-suspend OFF

Raw dumps: [`heap-inuse_space.txt`](heap-inuse_space.txt),
[`heap-inuse_objects.txt`](heap-inuse_objects.txt),
[`cpu-profile-top.txt`](cpu-profile-top.txt), [`meminfo.txt`](meminfo.txt),
[`goroutine.txt`](goroutine.txt), plus the `.pb` for `go tool pprof`.

| Metric | Value | Source |
|---|---|---|
| Live WG endpoints | **11** | config |
| `RoutineReceiveIncoming` (recv-workers) | **22** (2 × 11) | goroutine.txt |
| total goroutines | 527 | goroutine.txt |
| `RoutineDecryption` | 0 (lazy — no traffic mid-capture) | goroutine.txt |
| **`PopulatePools.func3` inuse_space** (messageBuffers pool) | **263.29 MB** | heap |
| `func3` inuse_objects | **4212 buffers** × 65535 B | heap |
| total heap inuse_space | 276.91 MB | heap |
| **CPU (process)** | **259.64 %** (26.33 s samples / 10.14 s) | cpu |
| **`runtime.scanobject`** | **56.06 % cum CPU** | cpu |
| GC functions total (scanobject+sweep+mark+findObject+…) | **~50 % flat** | cpu |
| Native Heap (dumpsys) | 36.6 MB | meminfo |
| TOTAL PSS / RSS | 397.5 MB / 403.9 MB | meminfo |

## Why this baseline matters

Two facts, both from the profiles above, on a **real** config with the feature OFF:

1. **11 idle-capable WG endpoints hold 263 MB of live buffers** in the
   `messageBuffers` pool (`PopulatePools.func3`) — the exact allocation RESEARCH.md
   fingerprinted as the RAM/GC-heat holder.

2. **Over half the core's CPU is the garbage collector** (`scanobject` 56 % cumulative;
   GC functions ~50 % flat), running **at idle, with no useful traffic**. The GC is
   busy scanning those 263 MB of buffers held by endpoints that carry no traffic.

This is the causal chain RESEARCH.md postulated — *many live WG → large buffer pool →
GC scans it every cycle → CPU burns → device heats* — now observed end-to-end on a real
device: the buffer pool (263 MB) and the GC cost (56 % CPU) are measured together, not
inferred.

Idle-suspend's job is to `Down()` the endpoints not on the active route, collapsing
that pool and removing the GC scan work. The "after" capture (feature ON) will be added
alongside this folder to quantify the CPU/GC drop, not just the memory drop.

## Reproduce

```
go tool pprof -top -inuse_space heap.pb | grep PopulatePools   # 263.29 MB
go tool pprof -top cpu.pb | grep scanobject                    # 56.06% cum
```
