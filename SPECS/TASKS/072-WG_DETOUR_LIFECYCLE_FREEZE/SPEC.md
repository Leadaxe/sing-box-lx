# SPEC: 072 — WG_DETOUR_LIFECYCLE_FREEZE

**Feature:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)
**Touches:** P11 + P12 — this task owns both promises (stopping a starting service must not crash the process; one dead detour node must not freeze the process's network machinery); P1 — the registry entry carries the removal conditions.

This task is the **single owner of the WG-endpoint lifecycle field-fault family**. It absorbs SPEC [070](../070-WG_START_CLOSE_RACE_CRASH/SPEC.md) (start/close race crash) and SPEC [071](../071-WG_BIND_DIAL_PAUSE_DEADLOCK/SPEC.md) (bind-dial/pause deadlock) — their directories are pointers here, their mechanisms live on unchanged under their original `// lx: SPEC 070` / `// lx: SPEC 071` markers (kept to avoid churn in upstream-adjacent files; all three marker numbers belong to this task) — and adds the two cuts the 2026-08-17 field dump proved missing. Merge rationale and lineage: [HISTORY.md](HISTORY.md).

| Field | Value |
|------|----------|
| Type | B (bug family) — daemon lifecycle races and unbounded waits around the WG endpoint and its detour transport; three field dumps, one mechanism per dump, each closing part of the family |
| Status | I (implemented) — all four mechanisms in tree; new cuts unit red/green; `-race` suites green; **pending field validation** on the reporting client |
| Branch | `lx` |
| Base | superproject `e50030345` (SPEC 070 + 071 shipped in `v1.14.0-lx.27-rc.2`; cuts 3–4 on top) |
| Related | [050](../050-URLTEST_ZOMBIE_RUN_SURVIVES_RESTART/SPEC.md) (conn deadlines; its dial-context guard, which cuts 3–4 were built on, was retired by [077](../077-XHTTP_DIAL_CTX_CONTRACT/SPEC.md) — the dial now parks until the raise), [061](../061-XHTTP_DIAL_DOWNLOAD_DEADLOCK/SPEC.md) (async download await this task keeps intact), [059](../059-XHTTP_XMUX/SPEC.md) (pooled connections; `fail` releases the slot), [046](../046-DNS_HIJACK_PACKET_LOOP_STALL/SPEC.md) (same "dead detour freezes unrelated machinery" class), [020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md)/[041](../041-WG_HANDSHAKE_GIVEUP_REBIND/SPEC.md) (suspend/rebind visits multiply exposure), [030](../030-FAST_BOX_SHUTDOWN/SPEC.md)/[047](../047-EARLY_RPC_NIL_ROUTER_CRASH/SPEC.md) (adjacent lifecycle windows) |

**Touches (code):**
- `protocol/wireguard/endpoint.go` (`Start` under `resumeMu` + `closing` gate) and `box.go` (`Close` CAS) — `// lx: SPEC 070`, unchanged here;
- `transport/wireguard/client_bind.go` (bounded dial) and `transport/wireguard/endpoint.go` (detached pause dispatch) — `// lx: SPEC 071`, unchanged here except the load-bearing comment;
- `transport/v2rayxhttp/conn.go` (raise-failure pipe break, conn-scoped request contexts, packet-up post bound) — `// lx: SPEC 072`, fork-native package;
- tests `protocol/wireguard/endpoint_start_close_race_lx_test.go`, `lx-test/startclose/`, `transport/wireguard/client_bind_dial_timeout_lx_test.go`, `transport/wireguard/endpoint_pause_dispatch_lx_test.go`, `transport/v2rayxhttp/raise_failure_test.go`.

**The `sing` dependency, the wireguard-go submodule and the daemon are not touched.**

## Problem — three field dumps, one family

The profile shared by all three reporters: WireGuard endpoints over a `detour`
(VLESS + XHTTP), huge outbound groups, aggressive start/stop/reload usage.

1. **2026-08-12** (core `lx.25-rc.3`, 54-minute dump): `ClientBind.connect()`
   dialed the detour with no deadline while holding `connAccess`; the dial
   ended in an `io.Pipe.Write` nobody would read; the `sing` pause manager ran
   callbacks under its own lock. One half-alive node froze sends, closes,
   rebinds and process-wide pause delivery. → mechanisms 1–2 (shipped as
   SPEC 071, together with the SPEC 070 crash gate from the 2026-08-16
   report).

2. **2026-08-16** (core `lx.27-rc.1`, crash): stop during a slow start —
   `CloseService` runs `Box.Close` concurrently with a still-running
   `Box.Start`, the SPEC 020 `closeTunDevice` nil-assign made it a
   deterministic SIGSEGV. → mechanism 1 (shipped as SPEC 070).

3. **2026-08-17** (core `lx.27-rc.2`, 38-minute dump — **with both fixes in
   the binary**): the family closed ranks around two holes this task cuts:

   - **The raise-failure stand-down hole.** g275: `connect()` armed its 15 s
     `dialCtx`, the XHTTP dial handed the conn up, `vless.WriteRequest` wrote
     the protocol header into the upload pipe — and the pooled HTTP connection
     failed the `RoundTrip` without ever adopting the request body (x/net
     http2 does not close `req.Body` on pre-adoption errors). The error branch
     of `dialStreamOne` closed `created` via `setupReader(nil, err)`; the SPEC
     050 guard **stands down on `created` regardless of success**, so nothing
     was left to break the pipe and the deadline had nothing to cancel. The
     Write blocked 38 minutes holding `connAccess` **and** `device.net.RLock`
     (taken by `Peer.SendBuffers`, peer.go:167). Behind it: g305
     (receive→connect) on `connAccess`; g927 (detached pause apply →
     `Device.Down` → `BindClose`) on `device.net.Lock` while holding
     `pauseOpAccess`; g934 (`Endpoint.Close`) on `pauseOpAccess`; g52
     (`StartOrReloadService → Box.Close → endpoint.Manager.Close`, locked to
     the gomobile thread) holding the manager mutex; 11 goroutines in
     `Manager.Get` behind it — every `DetourDialer.init` in the process. The
     old box half-closed, the new one never started: total traffic death,
     `direct` included, while the app's probe (a separate session) kept
     passing. The SPEC 071 detached-pause dispatch did its exact job — the
     pause *manager* stayed live — but the close chain still welded.

   - **The deadline ride-along cycle.** XHTTP requests rode the DIAL context
     (`newRequest(ctx)`), and http2 binds the whole stream lifetime to the
     request context. The SPEC 071 deadline therefore aborted every **healthy**
     detour conn 15 s after connect — by design of the cancel-lifetime it was
     supposed to be a no-op after the raise. The endpoint reconnected in a
     permanent 15 s cycle (the dump's stuck dial is generation ~4, one minute
     after box start), re-rolling the raise dice against a rotting XMUX pool
     until the stand-down hole hit. Packet-up inherited worse: every upload
     POST captured the dial context — posts died with it (WG cycling), or,
     under an unbounded dial context, a wedged pooled connection blocked a
     Write forever, and `Close` could not abort the pending download RoundTrip
     ("torn down via the dial context instead" — which an unbounded context
     never fires).

## Root causes (state after SPEC 070/071)

- **(C) Raise-failure paths did not break the upload pipe.** The 050 guard
  covers exactly one window — dial-context cancellation before `created` —
  and `created` closes on FAILURE too. Every path that fails the raise
  (RoundTrip error, non-200, dead pooled connection) left a blocked or future
  Write unkillable. This is the direct root of the 38-minute freeze.
- **(D) Stream lifetime was welded to the dial context.** Correct for a
  connection-scoped unbounded ctx (the upstream v2rayhttp shape), wrong the
  moment a caller legitimately bounds the dial — which SPEC 071 made the WG
  bind do. The 15 s cycle is fix-induced exposure: it multiplied visits into
  (C) by ~×240/hour per endpoint.

Our own contribution mirrors 071's framing: the mechanisms are defects in our
fork-native transport package; SPEC 071's bound made one of them hot.

## Fix — four mechanisms, current state

1. **Start/close race gate** (`protocol/wireguard/endpoint.go`, `box.go` —
   `// lx: SPEC 070`, unchanged): `Start(stage)` is a `resumeMu` critical
   section with a `closing` gate; `Box.Close` idempotency is an atomic CAS.

2. **Bounded bind dial + detached pause** (`transport/wireguard/` —
   `// lx: SPEC 071`, unchanged): `connect()` dials under
   `WithTimeout(bindCtx, C.TCPTimeout)`, cancel released on generation death;
   `onPauseUpdated` dispatches `Down`/`Up` asynchronously, latest-event-wins
   under `pauseOpAccess`, `Close`/`Teardown` bump the stamp.

3. **Raise-failure pipe break** (`transport/v2rayxhttp/conn.go` —
   `// lx: SPEC 072`): every path that fails the raise goes through
   `fail(err)`, which binds the reader error (as before) **and breaks the
   upload pipe from the read half** — a blocked or future Write surfaces the
   actual failure instead of hanging (io.Pipe's first-error-wins keeps the
   root cause visible). `fail` also cancels the conn context and releases the
   pooled connection: the sing-vmess early dials return the conn together
   with the write error and callers drop it, so the error path cannot rely on
   a later `Close`. `splitConn.uploadFailed` now breaks from the read half
   too (a write-half `CloseWithError` hands the writer a bare
   `ErrClosedPipe`, losing the cause). Covers stream-one, stream-up
   (download- and upload-side), packet-up (download side; no pipe — the conn
   context cancel fails posts instantly).

4. **Conn-scoped request contexts** (`transport/v2rayxhttp/conn.go` —
   `// lx: SPEC 072`): requests ride `WithCancel(transport ctx)`, cancelled
   by `Close`/`fail` — never the dial context. The dial context bounds the
   RAISE only, by parking the dial itself: since SPEC 077 `DialContext`
   returns a stream-one/stream-up conn only once the HTTP layer has adopted
   the upload body (called Read on the pipe); a raise that fails before that
   fails the dial with its cause, a dial context that ends before that fails
   the dial with the context error — pipe broken, conn context cancelled (the
   pending RoundTrip dies instead of leaking), pooled slot released. After
   the return the dial context has no effect on the conn (`net.Dialer`
   contract; the DNS transport pool relies on it — SPEC 077). Packet-up
   deliberately does not wait — it has no upload pipe, and its download
   response may legitimately arrive only after the first upload (SPEC
   061/002), so a cancelled dial tears down via `Close`. Packet-up posts get
   their own per-exchange bound (`packetUpPostTimeout = C.TCPTimeout`) —
   posts must not die with the dial context but must not be unbounded either,
   or a wedged pooled connection blocks the WG send path; `packetConn.Close`
   cancels the conn context, aborting the pending download RoundTrip it
   previously leaked. (Until SPEC 077 the raise was bounded from OUTSIDE the
   dial by the SPEC 050 guard `watchDialContext`, armed on the dial context
   until `created`; HISTORY.md records why that form was retired.)

Mechanism 4 restores the load-bearing claim of mechanism 2 ("with the dial
bounded, every wait in the chain is bounded") that hole (C) falsified, and
removes the 15 s cycle that fed it. The pairing comment in `client_bind.go`
names the contract from the WG side.

## Deliberately not done, and why

- **No lock rework in `adapter/endpoint.Manager.Close`** (holds the map mutex
  across component closes; the dump's 11 `Manager.Get` waiters queue behind
  any close). With the root wait bounded, that queue is bounded (≤ the dial
  bound) — restructuring an upstream manager for a transient stall is merge
  debt, same reasoning as SPEC 070's refusal to touch
  `daemon/started_service.go`.
- **No `sing` fork, no wireguard-go lock rework, `connAccess` still held
  across the bounded dial** — SPEC 071's rationale stands unchanged.
- **No patch to sing-vmess's conn-drop-on-error dials** (external module,
  plain require): neutralised from our side — `fail()` cleans up everything
  the dropped conn owns (pipe, context, pooled slot).
- **Upstream `transport/v2rayhttp` keeps its request-ctx shape.** Its callers
  never hand it a bounded-but-outliving dial context; ours does (SPEC 071).
  The divergence lives entirely in our fork-native package.

## Verification

- **Unit, red/green** (`transport/v2rayxhttp/raise_failure_test.go`; red run
  recorded on the pre-fix base, each failing exactly on its class):
  - `TestStreamOneDialFailsOnRoundTripError` / `...OnBadStatus` (recorded
    in their pre-077 form `...WriteFreedOn...`) — red: «Write still blocked
    after the raise failed — upload pipe was not broken» (the 38-minute
    field mechanism, distilled). Since SPEC 077 the same raise failure ends
    the dial itself with the cause; the tests assert that form.
  - `TestStreamUpDialFailsOnDownloadError` (pre-077 `...WriteFreedOn...`) —
    red: same, split mode.
  - `TestStreamUpDialFailsOnUploadError` (pre-077
    `TestStreamUpWriteCarriesUploadError`) — red: «Write error "io:
    read/write on closed pipe" lost the upload failure».
  - `TestStreamOneConnSurvivesDialContextExpiry` — red: «Read after dial
    deadline: context deadline exceeded — dial ctx still bounds the live
    stream» (the 15 s cycle, distilled; live h2c echo server).
  - `TestPacketUpPostsSurviveDialContextExpiry` — red: «Write after dial
    deadline: context deadline exceeded — posts still ride the dial ctx».
  - `TestPacketUpPostBounded` — red: «Write not bounded: post still blocked
    on a wedged pooled connection».
  - `TestPacketUpCloseAbortsPendingDownload` — red: «pending download
    RoundTrip not aborted by Close — its context never died».
- **Absorbed tasks' units stay green, untouched:** start/close gate
  (`endpoint_start_close_race_lx_test.go`, `lx-test/startclose/`), dial bound
  (`client_bind_dial_timeout_lx_test.go` — including
  `TestConnectDialContextReleasedOnConnClose`, whose contract mechanism 4
  leans on), pause dispatch (`endpoint_pause_dispatch_lx_test.go`).
- **Suites:** `go test ./transport/v2rayxhttp/ -race`,
  `go test ./transport/wireguard/ -race -tags with_gvisor`,
  `go test ./protocol/wireguard/ -race` — green; `gofmt` clean.
- **Pending (field):** a build on the 2026-08-17 reporter's profile — expect:
  no 15 s reconnect cycle on healthy detour conns; a dead pooled connection
  costs one failed dial (bounded, error logged) instead of a process freeze;
  stop/reload during the failure stays live.

## Removal conditions (HOTFIXES P1)

- **Mechanism 1** lifts when upstream serialises the service lifecycle
  (watch the unlock around `instance.Start()` in `daemon/started_service.go`
  and `Start`/`Close` in `protocol/wireguard/endpoint.go`; conflicts land on
  the `// lx: SPEC 070` markers).
- **Mechanism 2** lifts when upstream bounds the `ClientBind` dial itself
  (watch `connect()`), and its pause half when `sing` stops calling callbacks
  under `d.access` (watch `service/pause/default.go` on bumps).
- **Mechanisms 3–4** are fork-native (`transport/v2rayxhttp` exists only in
  this fork) — nothing to lift; they are the package's permanent contract.
  Any refactor of the dial/raise path must keep the invariants pinned by
  `raise_failure_test.go`: a failed raise breaks the upload pipe; stream
  lifetime never binds to the dial context; every packet-up post is bounded;
  `Close` aborts pending RoundTrips.
