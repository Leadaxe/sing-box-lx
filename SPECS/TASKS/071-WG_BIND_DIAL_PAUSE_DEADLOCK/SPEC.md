# SPEC: 071 — WG_BIND_DIAL_PAUSE_DEADLOCK

**Feature:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)
**Touches:** P12 — this task owns the promise (one dead detour node must not freeze the process's network machinery); P1 — the registry entry carries the removal condition. Relies on the SPEC [050](../050-URLTEST_ZOMBIE_RUN_SURVIVES_RESTART/SPEC.md) dial-context watchdog in XHTTP (already in tree, not modified); adjacent to [020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md)/[041](../041-WG_HANDSHAKE_GIVEUP_REBIND/SPEC.md), whose suspend/rebind triggers multiply visits into the ring (neither is modified).

| Field | Value |
|------|----------|
| Type | B (bug) — upstream lock architecture: `ClientBind.connect()` dials under `connAccess` on an unbounded context, and the `sing` pause manager runs subscriber callbacks while holding its own lock; our SPEC 020/041 machinery raises the fork's exposure well above upstream's |
| Status | I (implemented) — both fixes in tree, unit red/green for the dial bound, dispatch-mechanics units green, `-race` suites green; **pending field validation** on the reporting client |
| Branch | `lx` |
| Base | superproject `9558ceb27` (on top of SPEC 070) |
| Related | [050](../050-URLTEST_ZOMBIE_RUN_SURVIVES_RESTART/SPEC.md) (its `watchDialContext` is what makes a dial deadline able to break a blocked pipe write), [041](../041-WG_HANDSHAKE_GIVEUP_REBIND/SPEC.md) (`selfHealRebind → BindUpdate` is one of the ring's members), [020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md) (`Suspend → Down` is another), [046](../046-DNS_HIJACK_PACKET_LOOP_STALL/SPEC.md) (same "dead detour freezes unrelated machinery" class, different path), [070](../070-WG_START_CLOSE_RACE_CRASH/SPEC.md) (previous entry in this lifecycle-race family) |

**Touches (code):** `transport/wireguard/client_bind.go` (bounded dial —
`// lx: SPEC 071`), `transport/wireguard/endpoint.go` (detached pause
application — `// lx: SPEC 071`), tests
`transport/wireguard/client_bind_dial_timeout_lx_test.go`,
`transport/wireguard/endpoint_pause_dispatch_lx_test.go`.
**The `sing` dependency, the wireguard-go submodule and the daemon are not
touched.**

## Problem (field dump, 2026-08-12)

An Android client (LxBox 2.20.7, core `1.14.0-lx.25-rc.3`) captured a full
goroutine dump: 557 goroutines, 12 parked on mutexes for up to **54 minutes**.
The tunnel was frozen, network-change handling dead (the JNI
`UpdateDefaultInterface` callback stuck 7 minutes, locked to thread), URL-test
batches dragged in and never released. The session ended with a user
force-stop at RSS 497 MB. Every link below was verified against both the dump
and the sources; goroutine numbers refer to the dump.

The profile runs WireGuard endpoints over a `detour` (VLESS + XHTTP
stream-one) — the WG UDP flow is tunnelled through another proxy. The detour
node was half-alive: TCP accepts, then silence.

## The ring (all links verified)

1. **g292 — the anchor.** `RoutineReceiveIncoming → ClientBind.connect()`
   takes `connAccess` (`client_bind.go:80`), dials the detour
   (`client_bind.go:97`), and the dial ends in `vless.WriteRequest` writing
   the protocol header into XHTTP stream-one's upload pipe. The node is dead,
   the HTTP stream never rises, nobody reads the pipe — `io.Pipe.Write`
   blocks forever (54 min). The dial context is `bindCtx` =
   `context.WithCancel(c.ctx)` (`client_bind.go:125`): **no deadline, and the
   only canceller is `ClientBind.Close`**.

2. **g206 — sends starve.** `Peer.SendHandshakeInitiation → ClientBind.Send →
   connect()` parks on the same `connAccess`, 54 min.

3. **Close cannot break in.** `ClientBind.Close` cancels `bindCtx` *before*
   locking `connAccess` — but nothing on the bind-close paths ever got to
   call it: `closeBindLocked` (wireguard-go) holds `device.net.Lock` and
   waits in `netc.stopping.Wait()` for the receive goroutine of step 1 to
   exit (g20271, spawned by SPEC 041's `selfHealRebind → BindUpdate`);
   `Endpoint.Suspend → Down → BindClose` (g266) waits on that `net.Lock`
   while holding the **device state mutex**.

4. **The process-wide pause manager freezes.** `sing`'s `defaultManager`
   runs subscriber callbacks **while holding `d.access`**
   (`service/pause/default.go` — all four of
   `DevicePause/DeviceWake/NetworkPause/NetworkWake`). Our subscriber is
   `Endpoint.onPauseUpdated → device.Down()` — g18402 sits inside that
   callback, blocked on the state mutex g266 holds. `d.access` is therefore
   never released: g26840 (`NetworkPause`) queues behind it, and every
   `ClientBind.receive` parked in `pauseManager.WaitActive()`
   (`client_bind.go:139`, g19639 — a *different, healthy* WG device) waits
   for a wake that can never be delivered.

The ring spans **two WG devices joined by the shared pause manager**: device
A holds the pipe-blocked dial; device B's receive loop is parked in
`WaitActive`; the pause manager's lock — held by a callback stuck on device
A — is what welds them together. One half-alive detour node freezes the
network machinery of the whole process; only force-stop recovers.

## Root causes

Two independent upstream defects, either of which suffices for a freeze:

- **(A) Unbounded dial under `connAccess`** — `client_bind.go`. The dial
  context has no deadline; the blocked operation is an `io.Pipe.Write` that
  no context reaches on its own. Everything that needs the bind — sends,
  close, rebind — queues behind a lock whose holder can wait forever.
- **(B) Callbacks under the pause manager's lock** — `sing`
  `service/pause/default.go`. Any subscriber that blocks freezes
  pause/wake/network-change delivery for the entire process. `sing` is
  **not forked** in this repo (plain `require`, no replace) — this defect
  cannot be patched where it lives without adding a fourth fork submodule.

Our contribution is exposure, not the defects: SPEC 020 (`Suspend → Down`)
and SPEC 041 (`selfHealRebind → BindUpdate`) visit these upstream locks far
more often than vanilla upstream does — which is why the ring closed in our
field first. SPEC 061 fixed a different XHTTP dial hang (packet-up waiting
for the download response) and does not cover this pipe write.

## Fix

Two independent cuts; each alone breaks the ring, together they cover each
other.

### 1. Bounded dial in `ClientBind.connect()` (`client_bind.go`)

The dial runs on `context.WithTimeout(c.bindCtx, clientBindDialTimeout)`
(`clientBindDialTimeout = C.TCPTimeout`, 15 s — the same probe budget as
SPEC 052's netstack connect deadline, generous enough for slow-but-alive
detour chains). Applies to both branches (`DialContext` and `ListenPacket`).

The mechanism that makes the deadline effective is SPEC 050's
`watchDialContext` in the XHTTP dial: it arms on the dial context and breaks
the upload pipe from the read half if the context fires before the stream is
up. A bare `io.Pipe.Write` sees no context — **without the 050 watchdog a
dial deadline would not break this hang**; transports without such a guard
would still need one. This dependency is load-bearing and named here so a
future refactor of either side keeps the pair together.

Cancel-func lifetime is deliberate: the timeout context is released **when
the dialed connection generation dies** (`wireConn.done`), not when
`connect()` returns. Stream-one hands the conn up *before* the stream is
raised, and the 050 guard stays armed until `created` — cancelling on
return would tear down a healthy connection mid-raise. Letting the timer
run means: if the stream is still not up 15 s after the dial started, the
guard kills it (that is the timeout doing its job on the whole
raise-the-stream phase); once the stream is up the guard has disarmed and
the timer firing is a no-op.

Failure behaviour is the existing one: `connect()` returns an error,
`receive` logs and retries every second, `Send` surfaces the error to the
handshake machinery — a permanent freeze becomes a bounded retry loop.

### 2. Detached pause application in `Endpoint` (`endpoint.go`)

`onPauseUpdated` no longer executes `device.Down()/Up()` in the pause
manager's calling goroutine. It dispatches asynchronously with
**latest-event-wins** semantics:

- `pauseSeq.Add(1)` stamps the event; a goroutine takes `pauseOpAccess`,
  re-checks the stamp, and only the newest event applies —
  out-of-order goroutine scheduling cannot leave the device in a stale
  state, and a burst of pause/wake flips coalesces to its final state.
- `Close()` and `Teardown()` bump `pauseSeq` and shut the device down
  **under `pauseOpAccess`** — a queued application can never touch a device
  that is being closed (previously that safety came implicitly from
  `UnregisterCallback` waiting out the in-flight callback under `d.access`;
  detaching the work moves the guarantee here).
- The Wake path keeps the SPEC 020/007 `suspended` gate unchanged: a
  suspended device stays down through pause/wake cycles.

The pause manager's `d.access` is now held only for the microseconds of a
goroutine spawn: no subscriber of ours can freeze process-wide pause
delivery again, wherever the device machinery happens to be stuck.

## Deliberately not done, and why

- **No fork of `sing` to move `emit()` out from under `d.access`.** That is
  the correct upstream fix, but it means a fourth fork submodule paid for on
  every merge (the rc.5 lesson), for a defect we can neutralise from our
  side of the callback boundary. If a future field incident shows *another*
  subscriber blocking under `d.access`, that is the moment to revisit.
- **No lock rework in wireguard-go** (`closeBindLocked`, `net.Lock`,
  `stopping.Wait`). With the dial bounded, every wait in that chain is
  bounded too; restructuring a foreign lock hierarchy for no additional
  guarantee is merge debt.
- **`connAccess` is still held across the (now bounded) dial.** Moving the
  dial outside the lock would let sends observe a half-connected bind and
  needs a singleflight redesign; with a 15 s ceiling the hold is tolerable
  and the change stays a hotfix.
- **Other endpoints' pause subscribers untouched** — a sweep found no other
  `pause.RegisterCallback` with blocking device work (the bridge/tun
  callbacks are network-monitor, not pause). MASQUE/OpenVPN netstacks do not
  subscribe.

## Verification

- **Unit, red/green** (`client_bind_dial_timeout_lx_test.go`):
  - `TestConnectDialBounded` — a dialer that blocks until its context fires
    (the ctx-aware stand-in for "050 watchdog breaks the pipe"); with the
    test override of `clientBindDialTimeout`, `connect()` must return an
    error within the bound and release `connAccess` (asserted by
    re-acquiring it). **Red on the pre-fix base** (verified in a pre-fix
    worktree): `connect()` never returns — the test fails with the
    starvation message after its 3 s ceiling.
  - `TestConnectDialContextReleasedOnConnClose` — after a successful dial,
    the timeout context must stay alive until the `wireConn` dies (the 050
    guard window), then be released.
- **Unit, dispatch mechanics** (`endpoint_pause_dispatch_lx_test.go`):
  - `TestOnPauseUpdatedNeverBlocks` — with `pauseOpAccess` held (a stuck
    device stand-in), `onPauseUpdated` returns immediately; the pause
    manager's goroutine is never captive again.
  - `TestPauseEventsLatestWins` — a burst of events applies exactly once,
    with the last event (test hook seam, precedent `resumeErrHook`).
  - `TestPauseEventInvalidatedByShutdownStamp` +
    `TestCloseAndTeardownBumpPauseStamp` — an event queued behind
    `pauseOpAccess` must not apply after the shutdown stamp, and both
    `Close` and `Teardown` do bump that stamp (split this way to keep the
    ordering deterministic; a live Close-vs-event race is inherently
    timing-dependent).
- **Suites:** `go test ./transport/wireguard/ -race -tags with_gvisor` and
  `go test ./protocol/wireguard/ -race` green; `gofmt` clean.
- **Pending (field):** a build on the reporting client's profile — expect a
  half-alive detour node to cost a 15 s retry loop on its own endpoint and
  nothing process-wide; network switching stays live throughout.

## Removal condition (HOTFIXES P1)

- Fix 1 lifts when upstream bounds the `ClientBind` dial itself (watch
  `connect()` in `transport/wireguard/client_bind.go` on every merge — an
  upstream rewrite of the dial block will conflict on the marker).
- Fix 2 lifts when upstream `sing` stops calling pause callbacks under
  `d.access` (watch `service/pause/default.go` on `sing` version bumps) —
  the detached dispatch then guards nothing, though it stays harmless.
