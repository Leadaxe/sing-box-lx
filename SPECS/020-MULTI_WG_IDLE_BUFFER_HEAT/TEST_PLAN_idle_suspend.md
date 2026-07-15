# SPEC 020 — Idle-suspend live test plan (Down/Up model)

Branch: `lx-spec020-idle-suspend`. This validates the **shipped** idle-suspend
(commits `c55cf11e` core, `ace08f7a` event cache, `239c0515` tests). Goal: on a
real run, confirm an idle + unreachable WG/AWG endpoint is brought `Down`
(freeing its recv-worker `bufsArrs`), logged, and woken on the next dial.

Companion design: [SPEC.md](SPEC.md).
Authoritative root-cause: [RESEARCH.md](RESEARCH.md) (heap A/B: holder = `bufsArrs`).

---

## What is being verified

1. **Suspend fires.** A WG/AWG endpoint that is idle past `lx_idle_suspend` AND
   unreachable from the active routing tree goes `Down`. Observable as an INFO log
   `lx idle: suspend <tag> idle=<dur>` (edge-triggered — one line per transition).
2. **Reachable endpoints are NEVER suspended.** The `final` outbound and any rule
   target / active selector choice / active urltest pool member stay up — no
   suspend log for them, ever.
3. **Wake on dial.** Routing traffic through a suspended endpoint wakes it:
   INFO `lx idle: wake <tag> by=dial`, then traffic flows (a fresh handshake is
   expected — this is the Down model).
4. **Memory drop.** With many WG endpoints, suspending the idle/unreachable ones
   reduces `bufsArrs` live bytes (the §208 holder). Measure via pprof heap if the
   build exposes a debug/pprof endpoint; otherwise RSS as a coarse proxy.
5. **No flapping.** Over a few minutes idle, an endpoint should suspend once and
   stay suspended (no suspend/wake churn in the log). Count suspend↔wake pairs.

---

## Build (isolated — do not touch any running launcher)

```
cd /Users/macbook/projects/sing-box-lx
go build -tags "with_gvisor,with_wireguard,with_quic" \
  -o /tmp/sing-box-lx-test ./cmd/sing-box
```
(The `-lobjc` linker warning on macOS is benign.)

Generate keys if needed: `/tmp/sing-box-lx-test generate wg-keypair`.

---

## Real nodes available (provided by user — WARP/AWG + plain WG)

These are live subscription URLs to convert into sing-box `endpoints`. Mix of
`wireguard://` (plain WG + WARP-AWG via `jc/jmin/jmax/s1/s2/h1..h4/i1..` query
params → AmneziaWG) and `awg://`:

- WARP AWG 1.5 — `188.114.97.5:891` (id=gosuslugi.ru, ip=quic, jc=4 …)
- wg-parnas (plain WG) — `212.232.78.237:51820`
- awg2-home (AWG) — `93.100.173.230:51821` (jc=10, i1/i2 magic headers, presharedkey)
- WARP AWG 1.5 #2 — `188.114.97.4:4177`
- WARP AWG 1.5 #4 — `188.114.96.4:891`
- WARP AWG 1.5 #5 — `162.159.195.5:928`
- WARP AWG 1.5 #6 — `188.114.98.10:1014`
- WARP AWG 1.5 #7 — `162.159.195.8:1387`

(Full URLs are in the chat that produced this plan; each carries
privatekey@host:port plus query params: address, publickey, allowedips, mtu,
keepalive/presharedkey for WG; jc/jmin/jmax/s1..s4/h1..h4/i1..i4/ib/id/ip for AWG.)

Convert each URL → a `wireguard` endpoint object:
- `private_key` = the userinfo before `@`
- `server` / `server_port` = host:port
- `peer_public_key` = `publickey` param
- `local_address` = `address` param (split on comma)
- `pre_shared_key` = `presharedkey` (awg2-home only)
- `persistent_keepalive_interval` = `keepalive`
- `mtu` = `mtu`
- AmneziaWG block (when jc/etc present): `jc, jmin, jmax, s1, s2, s3, s4,
  h1, h2, h3, h4, i1, i2, i3, i4` → under the endpoint's amnezia/awg options
  (check `option/wireguard*.go` for exact field names in this fork).

---

## Test config shape (the key scenario)

Build an `endpoints` array with ALL ~8 nodes, then make MOST of them
**unreachable** so the tick suspends them, and ONE reachable so it must NOT be:

```jsonc
{
  "log": { "level": "info", "timestamp": true },
  "endpoints": [ /* wg-1 ... wg-8 from the URLs above, tags "wg-1".."wg-8" */ ],
  "outbounds": [
    { "type": "direct", "tag": "direct" }
  ],
  "route": {
    "lx_idle_suspend": "8s",          // SHORT threshold for a fast test
    "final": "wg-1",                  // wg-1 is reachable; wg-2..wg-8 are NOT
    "rules": []                       // no rule targets → only wg-1 reachable
  }
}
```

Tick period = `max(8s/2, 5s)` = **5s**. So within ~8–13s of startup, `wg-2..wg-8`
(idle + unreachable) must each emit one `lx idle: suspend` line; `wg-1` must not.

Variations to also run:
- **Selector scenario:** put `wg-1..wg-3` in a `selector` outbound, `final` = the
  selector. Only the selector's `Now()` (default = first member) is reachable; the
  other two members must suspend. Then switch the selector (via clash/command API
  or by editing default) → confirm the cache invalidates (newly-deselected node
  becomes suspendable, newly-selected wakes on its next dial).
- **urltest scenario:** put several in a `urltest` group (round_robin if this fork
  supports `mode`); confirm the WHOLE active pool stays reachable (no suspend) and
  only out-of-pool nodes suspend.
- **Disabled kill-switch:** set `lx_idle_suspend` to `0` / omit it → confirm the
  tick never runs (zero suspend logs, current behaviour).

---

## Run + capture

```
/tmp/sing-box-lx-test run -c /tmp/idle-test.json 2>&1 | tee /tmp/idle-run.log
```

Let it sit **idle for ~60s** (do not route traffic), then:

1. **grep the transitions:**
   ```
   grep "lx idle:" /tmp/idle-run.log
   ```
   Expect: one `suspend wg-N` per unreachable node, none for `wg-1`.

2. **Wake test:** drive a request through a suspended endpoint. Easiest: add a
   temporary rule routing some domain to `wg-2`, `curl --proxy` via a mixed inbound,
   or use the command/clash API to select it. Expect `lx idle: wake wg-2 by=dial`
   in the log, followed by traffic (handshake then data).

3. **Memory (if pprof available):** add `"experimental": { "debug": { "listen":
   "127.0.0.1:8965" } }` (check this fork's debug option name) and capture heap
   before/after the suspend window:
   ```
   curl -s 'http://127.0.0.1:8965/debug/pprof/heap?gc=1' > /tmp/heap-before.pb.gz  # ~T+3s, before suspends
   # wait past the 8s threshold + a tick
   curl -s 'http://127.0.0.1:8965/debug/pprof/heap?gc=1' > /tmp/heap-after.pb.gz   # ~T+20s, after suspends
   go tool pprof -top -inuse_space /tmp/heap-after.pb.gz | grep -i "PopulatePools\|RoutineReceiveIncoming\|bufsArrs"
   ```
   Expect `RoutineReceiveIncoming` / `PopulatePools` inuse_space to drop roughly in
   proportion to the number of suspended endpoints (each recv-worker frees ~8 MB
   bufsArrs on Down → recv-worker exit; 2 workers/device).
   If no pprof: record process RSS (`ps -o rss= -p <pid>`) before/after as a coarse signal.

---

## Pass criteria

- [x] Unreachable + idle endpoints log exactly one `suspend` each within ~2 ticks.
- [x] `final` / reachable endpoints log NO suspend.
- [x] Dialing a suspended endpoint logs `wake by=dial` and then passes traffic.
- [x] No suspend↔wake flapping over a 2-minute idle hold (≤ the expected count).
- [x] (If pprof) recv-worker / PopulatePools inuse_space drops after suspends —
      measured as **recv-worker goroutine count 16→0** + **RSS −31%** (see §RESULTS).
- [x] `lx_idle_suspend: 0` → zero `lx idle:` lines (kill-switch).

**All pass criteria met on a live macOS run (2026-07-01). See §RESULTS below. A
pre-existing bug — the idle tick iterated the wrong manager and never reached any
endpoint — was found by this very run and fixed; see SPEC.md §11.**

---

## Known expectations / caveats

- **Handshake on wake is EXPECTED** (Down zeroes the crypto session — see §13.2).
  A small latency on the first packet after wake is correct, not a bug.
- **WARP nodes may not complete a handshake** without the right client identity;
  that does NOT affect the suspend test — an endpoint that never handshakes is
  still `Up` with live recv-workers/bufsArrs until suspended, which is exactly the
  idle case we want to suspend. Suspend/wake logging is independent of whether the
  tunnel data-plane works.
- **AWG over WireGuard guard** (`SPEC 007`) is orthogonal: if any AWG endpoint
  detours through a WG one, it is guard-suspended (started=false) and must NOT be
  idle-woken — verify no `wake` line resurrects a guard-suspended endpoint.
- Reduced-bind urltest wake (8-buffer, no GRO) is **deferred** (§13.3/13.4) — not
  in this build; do not test for it.

---

## Report back

Paste: the `grep "lx idle:"` output, the suspend/wake counts, and (if captured)
the pprof before/after `RoutineReceiveIncoming` numbers. That closes the
device-verification gap noted in RESEARCH.md and tells us whether to build the
reduced-bind / no-handshake iteration (only if flapping is observed).

---

## RESULTS — live run 2026-07-01 (macOS desktop, all 8 user nodes)

Binary: `with_gvisor,with_wireguard,with_quic,with_awg,with_utls,with_clash_api`.
Node liveness (probed via clash `/proxies`): **wg-1** (WARP, ~100–300 ms),
**wg-2** (parnas plain-WG, ~60 ms), **wg-3** (home AWG, ~70 ms) are live; **wg-4…wg-8**
(WARP) never handshake (n/a) — expected, no client identity. Suspend/wake logging is
independent of data-plane liveness, so dead WARP nodes are still valid suspend subjects.

### ⚠️ Bug found & fixed by this run

The first run produced **0 `lx idle:` lines** — the feature was inert. Root cause:
the idle tick iterated `outbound.Outbounds()`, which never lists WG/AWG endpoints
(they live in the endpoint manager). Fixed by iterating the endpoint manager too
(SPEC.md §11). A regression test (`route/idle_tick_endpoints_lx_test.go`) now covers
this seam. All results below are post-fix.

### Reachability matrix (each = its own live config, `lx_idle_suspend: 8s`, tick 5 s)

| Scenario | Config | Reachable (NOT suspended) | Suspended | Verdict |
|---|---|---|---|---|
| **selector** | `select:[wg-1,wg-2,wg-3]`, final=select | wg-1 (`Now()`) | wg-2..wg-8 (7) | ✅ |
| selector switch | clash select → wg-2 | wg-2 (new `Now()`) wakes by=dial | wg-1 (old `Now()`) re-sleeps next tick | ✅ dynamic invalidation |
| **rule target** | rule `→ wg-3`, final=other | wg-3 (rule seed) | non-targets | ✅ |
| **detour chain** | `http` ob `detour:wg-2`, final=that | wg-2 (`Dependencies()`) | others | ✅ |
| **urltest round_robin pool** | `ut` pool:2 over [wg-1,wg-2,wg-3] | active pool (2 nodes) | evicted member + out-of-group | ✅ whole pool, not just Now |
| urltest probe-wake | force `/proxies/wg-1/delay` on a sleeping out-of-pool node | — | wg-1: suspend → **wake by=dial** (delay=189) → **re-suspend** | ✅ probe wakes, idle-tick re-sleeps (§4.3) |
| **legacy urltest** | `ut-legacy:[wg-5,wg-6]` least_test | wg-6 (`Now()`, by delay) | wg-5 (non-selected member) | ✅ |
| **cycle** | `sel-A:[sel-B,..]`, `sel-B:[sel-A,..]` | — | — | ✅ config validator rejects (`circular outbound dependency`); walk cycle-guard also unit-tested (`TestReachableCycleGuard`) |

### Suspend / wake / flap

- **suspend fires** once per unreachable+idle endpoint, edge-triggered (`idle=10s` =
  2nd tick past the 8 s threshold).
- **reachable never suspends** (verified per scenario above).
- **wake by=dial** confirmed two ways: (a) selector switch → real HTTP 204 through the
  woken tunnel in 0.068 s; (b) urltest force-probe → `delay=189`, `wake … by=dial`.
- **no flapping**: +30 s idle hold → still N suspend / 0 wake, no new lines.
- **kill-switch**: `lx_idle_suspend: 0s` → **zero** `lx idle:` lines (tick never starts).

### Resource A/B (the headline) — 8 endpoints, final=direct, all unreachable → all suspend

| Metric | Before (8 Up) | After (8 Down) | Δ |
|---|---|---|---|
| `RoutineReceiveIncoming` goroutines | **16** (2/dev × 8) | **0** | −16 (the `bufsArrs` holder, freed) |
| total goroutines | 344 | 312 | −32 |
| process RSS | 39.3 MB | **27.0 MB** | **−12.3 MB (−31%)** |

Captured via `experimental.debug.listen` pprof: `/debug/pprof/goroutine` (recv-worker
count) + `ps -o rss`. The recv-workers exit on `device.Down()` exactly as designed
(SPEC §13.1), releasing their `bufsArrs`. `RoutineDecryption` stays 64 (the crypto
WaitPool survives Down/Up by design, §7.0). The gvisor netstack (~5.9 MB/dev, the
largest single heap holder in inuse_space) is **not** freed by Down — that is Tier-B
territory (§7.5), out of scope. On Android, where `BatchSize=128` makes each
`bufsArrs` ~8 MB, this recv-worker delta is far larger than the desktop figure here
(RESEARCH.md heap A/B), so −31% RSS on desktop is a conservative lower bound on the win.

**Conclusion: every reachability node type (final, rule, detour, selector Now,
urltest pool, legacy Now), suspend, probe-wake, dial-wake, re-sleep, no-flap, and the
kill-switch are confirmed live; and suspend measurably frees recv-workers / RAM. The
device-verification gap from RESEARCH.md is closed (desktop). An Android heap A/B per
[[lxbox-207-pprof-capture]] remains the only stronger evidence, and is not required to
ship.**

### Edge-case matrix (additional live runs)

| Edge case | Setup | Result |
|---|---|---|
| **AWG-over-WG guard ↔ idle** | AWG endpoint `detour:` a plain-WG one (guard fires at Start, `started=false`) | guard-suspended node **never appears** in any `lx idle:` line — not suspended (already down), not idle-woken (§8 invariant). Others suspend normally. ✅ |
| **nested selector→urltest→endpoints** | `sel-top` Now=`ut-mid` (round_robin pool:2) | walk descends 2 levels; whole active pool reachable; non-selected `sel-side` branch members suspend. ✅ |
| **dual-path reachability** | a node = selector `Now()` AND a rule target simultaneously | reachable (visited-set dedup), never suspends; non-Now sibling suspends. ✅ |
| **flap stress** | single node, repeated probe→wake→re-sleep | clean 1:1 suspend↔wake alternation over 6 cycles; goroutine baseline 50 unchanged after cycles, recv 0→2→0 each cycle → **no leak**. ✅ |
| **cyclic groups** | `sel-A↔sel-B` mutual reference | rejected at start by the config validator (`circular outbound dependency`); walk's own visited-set cycle-guard is unit-tested. ✅ |

### Production config (the user's real LxBox config, desktop-adapted)

The user's actual config — 11 WG/AWG endpoints, 5 groups including **a selector
(`vpn-1`, the `final`) that contains a round_robin urltest (`vpn-1-auto`, pool:4) as a
member**, a selector with `default` (`vpn-3`), `cache_file` sticky selection — run with
`lx_idle_suspend: 8s` (TUN→mixed, on-device rule-set paths inlined; topology untouched):

- **Base state**: `vpn-1` Now()=`🔥⛈️ WARP (AWG 1.5)` (first member) and `vpn-3`
  default = the same node, so **exactly that one endpoint is reachable**; the other
  **10 suspend**. The nested `vpn-1-auto` pool is dormant (it is a non-selected member
  of `vpn-1`). ✅
- **Switch `vpn-1` → `vpn-1-auto`** (nested urltest) via clash: cache invalidates, walk
  descends into the urltest's active pool, and **exactly 4 endpoints wake by=dial**
  (= `pool:4`) via the health-check. ✅

This is the strongest single test — the real nested-group topology behaves exactly as
the reachability model specifies.

### Unit coverage added (locks the live findings into the suite)

- `route/reachability_lx_test.go`: `TestReachableSelectorToURLTestPool` (nested
  selector→urltest whole pool), `TestReachableDualPathDedup`,
  `TestReachableSelectorMemberIsURLTestNotSelected` (dormant nested subtree).
- `route/idle_tick_endpoints_lx_test.go`: `TestIdleTick_scansBothManagers` (tick
  visits both endpoint + outbound managers).
- `protocol/wireguard/endpoint_idle_lx_test.go`:
  `TestSuspendIfIdle_guardSuspendedNotTouched` (§8 AWG-guard invariant).

All adversarially checked: breaking the selector walk (`Now()`→`All()`) fails the new
dedup/dormant-subtree tests; the both-managers test fails on the pre-fix
outbounds-only loop.

### Wake-latency series (does sleep hurt the user? — 50 dials, 2 analysts + 2 skeptics)

Single live node (parnas WG, ICMP RTT floor 4.8 ms). 25 COLD dials (node
idle-suspended — `recv-workers=0` confirmed before each — then woken by a selector
switch, first dial timed) vs 25 WARM dials (node already live). curl-total through the
tunnel to an HTTP-204 endpoint. Two independent statistical re-analyses + two hostile
skeptics (workflow `verify-idle-suspend-claims`).

| | median | min | p90 (no outliers) |
|---|---|---|---|
| COLD (first dial after wake) | **50–57 ms** | 33 ms | ~82 ms |
| WARM (already live) | **36 ms** | 25 ms | — |

- **Wake cost = median(cold) − median(warm) ≈ +14–21 ms ≈ ~3 RTTs** = one fresh WG
  handshake (Down zeroed the session, §13.2). Statistically real (Mann-Whitney
  p≈0.0002, bootstrap CI excludes 0) but **imperceptible** on a near server.
- Cold spikes of 0.35–0.79 s are gstatic HTTP/network jitter, **not** the tunnel — the
  WARM arm threw its own 7.14 s spike, proving the HTTP path injects that noise
  independently. Median (not mean) is the honest central estimate.
- suspend/wake events stayed **perfectly paired** (15/15, 22/22) — no flapping.

**Honest caveat (skeptic-forced):** because Down() zeroes keys, the wake handshake
scales with server RTT. On this 4.8 ms node it is a free +14 ms; on a routine
intercontinental node (~150 ms RTT) the same handshake projects to roughly **+450 ms on
the first packet** — a one-time perceptible stall, paid once per wake, latency only
(throughput is untouched: the active node's batch size never changes). The keys-safe
BindUpdate path (§13.3 path A) would remove even that far-server spike, but it is not the
shipped Down/Up mechanism. So: "wake is fast" is true on near servers; on far ones it is
"one sub-second first-packet handshake, then full speed."

---

## RESULTS — Android device run 2026-07-01 (CPH2411, Android 15, rc.18)

This closes the **device-verification gap** flagged in RESEARCH.md §"what is NOT done"
(the desktop A/B was −31% RSS; the Android heap A/B, where `BatchSize=128` makes each
`bufsArrs` ~8 MB, was projected but unmeasured). It is now measured.

Setup: LxBox app (2.8.0-dev.2) with the rc.18 AAR (`Libbox.version()` =
`1.14.0-lx.1-rc.18`, confirmed via the app's Debug API `/device`). Test config pushed
over the Debug API `PUT /config`: **9 WireGuard endpoints** — `wg-1` = a real WARP-AWG
node (reachable, `route.final`), `wg-2`..`wg-9` = 8 synthetic plain-WG (valid X25519
keys, peer at TEST-NET-1 `192.0.2.x` → never handshakes → live recv-workers until
suspended = exactly the idle case). `lx_idle_suspend: "30s"` (tick 15 s). pprof via the
app's libbox PProfServer (`/diag/pprof`), core logs via `/logs/core`.

> Gotcha (unrelated to this feature): on rc.18 a DNS server with `detour` to a
> settings-less `direct` outbound now fails start (`detour to an empty direct outbound
> makes no sense`). Simplified the test DNS to a plain UDP resolver.

### Reachability + suspend/wake/flap/kill-switch

| Check | Result |
|---|---|
| **suspend fires** | `wg-2`..`wg-9` (8 nodes) each logged one `lx idle: suspend wg-N idle=30s`, edge-triggered (all within the same tick). ✅ |
| **reachable never suspends** | `wg-1` (the `final`, carrying real traffic) never appears in any `lx idle:` line. ✅ |
| **wake by=dial** | `POST /action/urltest?tag=wg-2` (a dial through the sleeping node) → `lx idle: wake wg-2 by=dial`; recv-workers 2→4. ✅ |
| **no flapping** | suspend/wake pairs stayed balanced; no churn over the idle hold. ✅ |
| **kill-switch** | `lx_idle_suspend: "0"` → **zero** `lx idle:` lines over 48 s idle (tick never starts). ✅ |

### Resource A/B — the headline (9 endpoints, final=wg-1, 8 idle+unreachable → suspend)

Clean stop→start (fresh 18 recv-workers), heap+goroutine at T+5 s (all up), then again
after the 8 suspended (recv-workers 18→2).

| Metric | Before (9 up) | After (8 Down) | Δ |
|---|---|---|---|
| `RoutineReceiveIncoming` goroutines | **18** (2/dev × 9) | **2** (wg-1 only) | −16 |
| total goroutines | 417 | 389 | −32 |
| **`PopulatePools.func3` inuse_space** (the `bufsArrs` holder) | **223.93 MB** | **89.89 MB** | **−134 MB (−60%)** |
| total process `inuse_space` (kernel heap) | 232.75 MB | 99.21 MB | −133.5 MB |

`PopulatePools.func3` is the exact `make([]*[65535]byte, BatchSize)` allocation
identified in RESEARCH.md as the GC-heat / RAM holder. Suspending 8 idle+unreachable
endpoints freed **134 MB** of live heap. That is 16 freed recv-workers × ~8.4 MB/worker
(`BatchSize=128`) — matching the model exactly, and **~10× the desktop RSS delta** (where
`BatchSize` is small). This is the strongest single piece of evidence for the memory win,
and it now exists.

**Conclusion (Android):** every idle-suspend behavior confirmed on-device on rc.18 —
suspend, reachability (final stays up), wake-by-dial, no-flap, kill-switch — and the
headline memory saving is directly measured in `bufsArrs` inuse_space. The
RESEARCH.md device gap is closed on Android, not just desktop.

---

## §NEW — live-план ревизии 2026-07-15 (НЕ прогнан)

Юнит-тесты зелёные (`go test -race` route/, protocol/wireguard/, protocol/group/,
transport/wireguard/ с тегами и без); на устройстве проверить:

1. **Pause-wake не воскрешает.** Усыпить N недостижимых нод (лог `lx idle: suspend`),
   выключить/включить экран (или wifi↔cellular). Ожидание: НЕТ повторных suspend-строк
   (устройства не поднимались), recv-воркеры остаются 0 (goroutine profile), дайл через
   ноду по-прежнему будит (`lx idle: wake ... by=dial`).
2. **Transfer-гейт.** Начать долгую закачку через WG-ноду в round_robin-пуле, добиться
   вытеснения ноды из пула (или переключить селектор прочь). Ожидание: закачка живёт,
   нода НЕ гасится, пока идут байты; после завершения закачки — suspend через порог.
3. **Probe-гейт.** Селектор уходит с urltest-группы. Ожидание: в логе НЕТ probe-циклов
   группы после ухода (до idle_timeout тикер жив, но пробы пропущены); члены группы
   засыпают один раз и не флапают. Возврат селектора → пробы возобновляются немедленно.
4. **`lx_idle_suspend_reachable: "30m"`.** Ночной фон без трафика: члены пула гаснут
   через ~30m (лог), первый утренний дайл будит (+1 handshake), никакого probe-флапа
   (idle_timeout глушит пробы раньше).
5. **AWG guard на рестарте.** Селектор [vless, wg] + AWG detour→селектор: выбрать wg,
   перезапустить приложение. Ожидание: AWG-нода НЕ стартует (лог «will not start»),
   ядро живо, kernel hang отсутствует. Выбрать vless → рестарт → AWG работает.
6. **DNS-detour сид.** dns.server с detour на WG-ноду, трафик мимо неё: нода НЕ гаснет
   (или гаснет только по reachable-порогу), DNS-запросы без периодических +RTT спайков.
7. **Keepalive-иммунность гейтов.** Недостижимая WG-нода с `persistent_keepalive: 25`:
   засыпает через порог как обычно (keepalive/rekey-шум не держит её живой) — до
   ревизии transfer-гейта голая проверка «счётчики сдвинулись» не дала бы ей уснуть
   никогда. Отдельно: живая TCP-закачка через вытесненный узел (established>0) —
   узел НЕ гаснет до закрытия потока, независимо от объёма.
8. **`passive_check: true` (SPEC 019).** Активный браузинг через least_test-группу:
   в логе НЕТ periodic probe-циклов (выбранный узел пассивно подтверждён), спящие
   члены не будятся; убить выбранный узел → пробы возобновляются в ближайший тик
   после лапса сигнала (< interval), группа переключается.
