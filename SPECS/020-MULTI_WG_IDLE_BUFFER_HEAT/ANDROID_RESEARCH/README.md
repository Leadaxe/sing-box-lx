# SPEC 020 — Android device research (idle-suspend)

> On-device verification of `lx_idle_suspend` on **real Android hardware**, run
> through the LxBox app carrying the rc.18 core. This closes the device gap flagged
> in [`../RESEARCH.md`](../RESEARCH.md) §13 ("what is NOT done") and in
> [`../TEST_PLAN_idle_suspend.md`](../TEST_PLAN_idle_suspend.md) §"Known caveats":
> the desktop A/B was −31 % RSS; the **Android** heap A/B (where `BatchSize=128`
> makes each `bufsArrs` ~8 MB) was *projected but never measured*. It is now measured.

Files in this folder:

| File | What |
|---|---|
| [`README.md`](README.md) | this — the full report |
| [`METHOD.md`](METHOD.md) | reproducible method: how the core was built, how the test config was shaped, how measurements were taken |
| [`RESULTS.md`](RESULTS.md) | every scenario + the headline heap A/B, with raw numbers |
| [`artifacts/`](artifacts/) | raw evidence: core-log `lx idle:` lines, goroutine dumps, pprof heap `.pb` snapshots + their `-top` renders |

No access credentials (API tokens, device serials, IPs, private keys) appear
anywhere in this folder — the synthetic WG keys used were throwaway and are not
reproduced here.

---

## TL;DR

On a physical **CPH2411 (Android 15, arm64-v8a)** running the rc.18 core
(`Libbox.version()` = `1.14.0-lx.1-rc.18`), with **9 WireGuard endpoints** (1
reachable + 8 idle & unreachable) and `route.lx_idle_suspend: "30s"`:

- **Every behaviour confirmed:** suspend fires on the 8 idle+unreachable endpoints
  at exactly `idle=30s` (edge-triggered, one line each); the reachable `final` never
  suspends; a dial wakes a sleeping endpoint (`wake … by=dial`); no flapping; the
  kill-switch (`"0"`) produces zero `lx idle:` lines.
- **The headline — measured Android heap A/B:**

  | Metric | Before (9 up) | After (8 Down) | Δ |
  |---|---|---|---|
  | `RoutineReceiveIncoming` goroutines | **18** | **2** | **−16** |
  | `PopulatePools.func3` inuse_space (the `bufsArrs` holder) | **223.93 MB** | **89.89 MB** | **−134 MB (−60 %)** |
  | total process `inuse_space` | 232.75 MB | 99.21 MB | −133.5 MB |

  Suspending 8 idle+unreachable endpoints freed **134 MB of live heap**. That is 16
  freed recv-workers × ~8.4 MB/worker (`BatchSize=128`), matching the model exactly,
  and roughly **10× the desktop RSS delta** (where `BatchSize` is small). This is the
  single strongest piece of evidence for the memory win in the whole SPEC — and it now
  exists on the platform that motivated the feature.

---

## Why this run matters

`RESEARCH.md` identified the GC-heat / RAM holder on Android via a heap A/B, and
named it precisely: the recv-worker `bufsArrs` (`make([]*[65535]byte, BatchSize)`),
which on Android's `BatchSize=128` is ~8 MB per worker, 2 workers per WireGuard
endpoint. The whole idle-suspend design (`device.Down()`, not a light timer-stop)
was chosen *specifically* to release that holder.

But every A/B in `RESEARCH.md`/`TEST_PLAN` up to now was **desktop** — where
`BatchSize` is small, so `bufsArrs` is small, so the RSS delta was only −31 % / ~12 MB.
The doc is explicit that the Android heap A/B is "the only stronger evidence, and is
not required to ship." This run supplies exactly that evidence: on Android the same
`PopulatePools.func3` allocation is **224 MB** for 9 endpoints, and suspend collapses
it to 90 MB. The theory and the device agree to within the per-worker estimate.

## Scope / honesty

- The 8 sleeping endpoints were **synthetic** (valid keys, unreachable TEST-NET
  peers) so they never handshake. This is *deliberately the idle case the feature
  targets* — an endpoint that never handshakes is still `Up` with live
  recv-workers/`bufsArrs` until suspended (see `TEST_PLAN` §"Known caveats"). Suspend
  logging and the recv-worker/`bufsArrs` release are independent of whether the
  data-plane carries traffic.
- The 1 reachable endpoint (`wg-1`) was a **real** WARP-AWG node carrying live traffic
  — it exercises the "reachable never suspends" invariant against a genuinely active
  tunnel, not a stub.
- Measurements were taken through the app's Debug API pprof passthrough
  (`/diag/pprof`, the libbox `PProfServer`), i.e. the *same* pprof the core exposes —
  not a synthetic counter.

See [`METHOD.md`](METHOD.md) for the full reproducible procedure and [`RESULTS.md`](RESULTS.md)
for the per-scenario evidence.
