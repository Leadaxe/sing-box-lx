# SPEC 020 — Idle-suspend live test plan (Down/Up model)

Branch: `lx-spec020-idle-suspend`. This validates the **shipped** idle-suspend
(commits `c55cf11e` core, `ace08f7a` event cache, `239c0515` tests). Goal: on a
real run, confirm an idle + unreachable WG/AWG endpoint is brought `Down`
(freeing its recv-worker `bufsArrs`), logged, and woken on the next dial.

Companion design: [SPEC_idle_suspend_lever.md](SPEC_idle_suspend_lever.md) §13.
Authoritative root-cause: [SPEC.md](SPEC.md) (heap A/B: holder = `bufsArrs`).

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

- [ ] Unreachable + idle endpoints log exactly one `suspend` each within ~2 ticks.
- [ ] `final` / reachable endpoints log NO suspend.
- [ ] Dialing a suspended endpoint logs `wake by=dial` and then passes traffic.
- [ ] No suspend↔wake flapping over a 2-minute idle hold (≤ the expected count).
- [ ] (If pprof) recv-worker / PopulatePools inuse_space drops after suspends.
- [ ] `lx_idle_suspend: 0` → zero `lx idle:` lines (kill-switch).

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
device-verification gap noted in SPEC.md and tells us whether to build the
reduced-bind / no-handshake iteration (only if flapping is observed).
