# SPEC: 070 — WG_START_CLOSE_RACE_CRASH

**Feature:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)
**Touches:** P11 — this task owns the promise (stopping the service while it is still starting must not crash the process); P1 — the registry entry carries the removal condition. Rides entirely on the serialisation primitives SPEC [020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md)/[030](../030-FAST_BOX_SHUTDOWN/SPEC.md) already put into the protocol layer (`resumeMu`, `closing`); neither of those tasks is modified.

| Field | Value |
|------|----------|
| Type | B (bug) — upstream lifecycle race (`daemon.StartedService` allows `CloseService` while `instance.Start()` is in flight, and nothing below serialises `Box.Start` against `Box.Close`), made deterministically fatal by our SPEC 020 `closeTunDevice` nil-assign |
| Status | I (implemented) — gate + hardening in tree, red/green unit and race-smoke green (darwin `-race`); **pending field validation** on the reporting client |
| Branch | `lx` |
| Base | superproject `9558ceb27` |
| Related | [047](../047-EARLY_RPC_NIL_ROUTER_CRASH/SPEC.md) (same window, RPC side: `s.instance` is published before `Start()` completes), [030](../030-FAST_BOX_SHUTDOWN/SPEC.md) (`closing` flag this fix reuses), [020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md) (`resumeMu` + the `closeTunDevice` nil-assign that arms the panic) |

**Touches (code):** `protocol/wireguard/endpoint.go` (`Start` — `// lx: SPEC 070`),
`box.go` (`Close` idempotency — `// lx: SPEC 070`), tests
`protocol/wireguard/endpoint_start_close_race_lx_test.go`,
`lx-test/startclose/wireguard_start_close_race_lx_test.go`.
**Transport layer (`transport/wireguard/`) and submodules are not touched.**

## Problem (field report, 2026-08-16)

An Android client (LxBox, core `1.14.0-lx.27-rc.1`, go1.26.5 arm64) crashes the
whole process with:

```
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x70 pc=0x7e389380b8]

goroutine 50 [running, locked to thread]:
github.com/sagernet/sing-box/transport/wireguard.(*Endpoint).Start(...)
        transport/wireguard/endpoint.go:322
github.com/sagernet/sing-box/protocol/wireguard.(*Endpoint).Start(...)
github.com/sagernet/sing-box/adapter/outbound.(*Manager).startOutbounds(...)
...
github.com/sagernet/sing-box/daemon.(*StartedService).StartOrReloadService(...)
```

The same dump shows **goroutine 52 closing the very same `Box`**
(`0x7d901ea7e0` in both stacks):

```
goroutine 52 [select, locked to thread]:
github.com/sagernet/sing-box/adapter/endpoint.(*Manager).Close(...)
github.com/sagernet/sing-box.(*Box).Close(0x7d901ea7e0)
github.com/sagernet/sing-box/daemon.(*StartedService).CloseService(...)
```

The profile explains why the window is easy to hit: 1755 outbounds
(1553 vless + 129 hysteria2 + …) and **18 WireGuard endpoints** behind tun.
Startup of a profile this size is slow (the goroutine ages show the process
had lived ~1 minute); the user pressed stop while it was still starting.

## Root cause (full chain)

1. **The daemon deliberately unlocks around `instance.Start()`.**
   `StartedService.StartOrReloadService` publishes `s.instance`, then
   releases `serviceAccess` for the duration of `instance.Start()`
   (`daemon/started_service.go:256–262`) — by design, so a stop can
   interrupt a slow start instead of queueing behind it.

2. **`CloseService` passes the gate during `STARTING`.** It sees status
   `STARTING`, takes the just-published instance and runs
   `instance.Close()` → `Box.Close()` **concurrently with the still-running
   `Box.Start()`** (`daemon/started_service.go:287–300`). Nothing below —
   neither `Box` nor the component managers — serialises a component's
   `Start` against its `Close`. This race exists in pristine upstream.

3. **Our SPEC 020 nil-assign arms the panic.** The endpoint manager's close
   pass reaches the WireGuard endpoint: `protocol` `Close()` sets flags under
   `resumeMu`, then calls the transport `Close()`, whose `closeTunDevice()`
   sets `e.tunDevice = nil` (`transport/wireguard/endpoint_close_lx.go`,
   needed because Teardown may have already released the device). The start
   goroutine is meanwhile between `e.tunDevice.Start()`
   (`transport/wireguard/endpoint.go:304`, already passed) and
   `e.tunDevice.SetDevice(wgDevice)` (line 322). The interface value is now
   `(nil, nil)`; the method load through the nil itab faults at a small
   offset — `addr=0x70`, exactly the reported signal. Upstream's own `Close`
   does not nil the field, so upstream gets a quieter (but still racy —
   device set vs. close, `IpcSet` vs. close) interleaving; ours is
   deterministic once the window is hit.

4. **The protocol-layer `Start` never took the lock.** SPEC 020/030 already
   serialise every *runtime* transition — `SuspendIfIdle`, `TeardownIfSlept`,
   `resumeOnDial` (which runs the full rebuild + both start stages under
   `resumeMu`), and `Close` — and `Close` already signals intent via the
   `closing` flag before contending. The one lifecycle entry point left
   outside the mutex was `Start(stage)` itself
   (`protocol/wireguard/endpoint.go:190`). That is the entire gap.

## Fix

One mechanism, protocol layer only: **`Start(stage)` becomes a `resumeMu`
critical section with a `closing` gate** (`// lx: SPEC 070`):

```go
w.resumeMu.Lock()
defer w.resumeMu.Unlock()
if w.closing.Load() {
    return os.ErrClosed
}
```

- **Close before the stage** → the stage refuses with `os.ErrClosed`; the
  transport (whose `tunDevice` is already nil) is never entered. The error
  fails `Box.start`, whose cleanup `s.Close()` is an idempotent no-op, and
  `StartOrReloadService` sees status ≠ `STARTING` and returns cleanly.
- **Close during the stage** → `Close` sets `closing` (aborting any dial
  wakes) and blocks on `resumeMu` until the stage completes, then closes the
  fully-constructed device. No leak: the wg device set by the stage is torn
  down by that close; the next stage refuses at the gate.
- Holding `resumeMu` for a whole stage is bounded and precedented: the stage
  does no network waits (peer domain resolution is a deferred callback,
  `SetEndpointResolver`), and `resumeOnDial` already runs the same transport
  calls under the same mutex on the rebuild path. SPEC 030's fast-shutdown
  promise (P4) is unaffected.

Secondary hardening, found by walking the new error path (`// lx: SPEC 070`
in `box.go`): **`Box.Close` idempotency was a non-atomic
select-then-`close(s.done)`**. Two concurrent closers — precisely a user
stop racing `Box.Start`'s own error-path `s.Close()`, which this fix makes
routine — could both take the `default` branch and panic with «close of
closed channel». Replaced with an `atomic.Bool` CAS; `done` stays closed for
any future readers.

## Deliberately not done, and why

- **No serialisation in `daemon/started_service.go`.** Making `CloseService`
  wait out `instance.Start()` would fix the whole class (every component
  manager runs in the same window), but it rewrites the central locking of an
  actively-merged upstream file and changes stop latency semantics the
  upstream design clearly wanted (stop must not queue behind a slow start —
  with the field profile that would hang the stop button for the whole
  1755-outbound startup). The WireGuard endpoint was the only member with a
  hard crash. The stand run under `-race` names the residual members
  precisely — three benign-in-practice upstream data races outside the WG
  endpoint: `Box.debugHTTPServer` (write `box.go` preStart vs. read `Close`),
  sing-tun `defaultInterfaceMonitor` (`monitor_shared.go` Start vs. Close via
  `NetworkManager`), and `route.Router` fields (`router.go:226` Start vs.
  `:306` Close). None crashes; all are documented here as the follow-up
  surface. Revisit as its own task if one of them faults in the field.
- **Transport layer untouched.** All transport lifecycle callers now sit
  under `resumeMu` in the protocol layer (`Start` stages, `resumeOnDial`
  rebuild, `Close`, `Teardown`); adding a second mutex inside the transport
  would guard nothing and complicate every SPEC 020 path.
- **`os.ErrClosed`, not a custom error.** The refusal is not a fault — it is
  «you closed me first»; the daemon swallows it by design (status ≠
  `STARTING` → return nil), and a distinctive error would only add noise to
  the crash-free log.

## Verification

- **Unit, red/green** (`protocol/wireguard/endpoint_start_close_race_lx_test.go`):
  - `TestStartRefusedAfterClose` — `Close()` then `Start(stage)` for both
    stages on the nil-device harness. **Red on the pre-fix base with a nil
    panic inside the transport `Start`** (verified: the harness faults at
    the bind construction, the field endpoint at the tun-device dereference
    — the same unguarded entry); green = `os.ErrClosed`, transport never
    entered.
- **Sweep smoke** (`lx-test/startclose/wireguard_start_close_race_lx_test.go`,
  `with_gvisor && with_wireguard`; lives beside `lx-test/zombie` because the
  `test/` module resolves the fork submodules from the proxy and does not
  build):
  - `TestWGStartCloseRace_LX` — N live WG endpoints (black-holed loopback
    peers, as in SPEC 030's stand), `Box.Start` raced against `Box.Close`
    with a swept delay so the close lands in every phase of the start;
    asserts no panic and no hang, and hammers the `Box.Close` CAS (both the
    external close and the start-error-path close run). Run **plain, not
    under `-race`**: the detector confirms the WG endpoint is clean but
    additionally reports the three residual upstream races listed above —
    failing the run on exactly the class this task deliberately leaves in
    place. Full traces preserved in the task's first `-race` run.
- **Suites:** `go test ./protocol/wireguard/ -race` (56s, green),
  `go test -tags with_gvisor ./transport/wireguard/` green; `gofmt` clean
  on touched files.
- **Pending (field):** a build on the reporting client's profile — expect
  stop-during-start to end in a clean `IDLE` instead of a process death.

## Removal condition (HOTFIXES P1)

Upstream serialises the service lifecycle — either `StartedService` stops
closing a still-starting instance, or `Box`/component managers make
`Start`/`Close` mutually exclusive per component. Watch
`daemon/started_service.go` (the unlock around `instance.Start()`) and
`protocol/wireguard/endpoint.go` `Start`/`Close` on every merge; an upstream
rewrite of either will conflict on the `// lx: SPEC 070` markers rather than
merge silently. The `Box.Close` CAS lifts independently once upstream makes
`Close` concurrency-safe.
