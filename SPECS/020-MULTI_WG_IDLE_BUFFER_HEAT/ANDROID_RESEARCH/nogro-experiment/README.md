# LX_WG_NO_GRO experiment — GRO-off + small batch vs Down/Up

**Question:** SPEC 020 shipped idle-suspend (Down/Up) to free the recv-worker
`bufsArrs` (the RAM/GC heat holder). But is a simpler *global* fix possible —
just make every WG device use a small receive batch, so `bufsArrs` is tiny to
begin with? The blocker was GRO: while UDP-GRO receive is on (Android default),
the batch can't shrink (the coalesce-split path hardcodes a 128-slot array).

**Hypothesis:** disable GRO receive *and* shrink the batch together. If download
throughput on a real cellular/WARP link doesn't drop (RESEARCH.md's own throughput
measurement hinted the link, not batch, is the bottleneck at ~86 Mbps), then
GRO-off + batch-8 is a simpler alternative to Down/Up: no handshake-on-wake, no
reachability walk, `bufsArrs` small on *all* nodes always.

## The switch

`LX_WG_NO_GRO=1` (env, read once at tunnel start):
- forces `rxOffload = txOffload = false` (`conn/features_linux.go`)
- `StdNetBind.BatchSize()` → 8 instead of 128 (`conn/bind_std.go`)
- `msgsPool` sizes its array to 8 too (else bufs=8 vs msgs=128 desync)

One source of truth: `conn/lx_nogro.go` (`lxNoGRO()` / `lxBatchSize()`). Coupled
by design — a small batch with GRO on panics, GRO off alone saves nothing.
`=0`/unset = byte-for-byte stock. Experimental submodule branch
`lx-awg2-v003-nogro-exp`; main repo branch `lx-1.14-nogro-exp`.

## AAR

Built on-demand (not a release):
`gh workflow run lx-build.yml -f target=android-aar -f branch=lx-1.14-nogro-exp`
→ `libbox.aar` / `libbox-legacy.aar` as a workflow artifact. Embed in LxBox.

## Running the A/B

[`AB-download.sh`](AB-download.sh) — needs a rooted phone, a WARP endpoint active,
static arm64 curl, and the app's pprof endpoint (adb forward). It:
1. sets `LX_WG_NO_GRO=0` via the Zygote `wrap.<pkg>` prop, restarts the app,
   snapshots heap, runs 5× 50 MB download (stock);
2. sets `=1`, restarts, snapshots heap, runs 5× 50 MB download (experiment).

## Reading the result

| Outcome | Meaning | Action |
|---|---|---|
| nogro download ≈ stock, bufsArrs much smaller | GRO not load-bearing on this link | GRO-off + small-batch is a viable simpler global fix; consider promoting behind the mobile tag (no user config) |
| nogro download ≪ stock | GRO carries real throughput | keep Down/Up (rc.19); close this experiment |

Either result is a result. The desktop can't measure it (BatchSize=1 there — the
`linux||android` branch is inactive), so this must run on the device.
